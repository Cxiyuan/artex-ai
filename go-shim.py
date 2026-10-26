#!/usr/bin/env python3
"""
OpenCode Go 本地适配代理（容器内自启动，仅依赖 Python 标准库）

解决三件事：
  1. 路径归一化 —— artex 是把 base_url 直接拼 /chat/completions 的，
     这里统一映射到 Go 的真实端点前缀（填不填 /v1 都能通）；
     同时 /v1/models 会映射到 Go 真正存在的全局模型列表接口
     （openai 家族的 /openai/v1/models 返回 404，所以 artex 拉不到列表）。
  2. 注入 x-opencode-session —— Go 要求该头；客户端已经带了就原样保留，
     缺失时才用本进程的稳定 UUID 兜底。
  3. 模型列表过滤 —— 只返回 openai chat/completions 协议族可用的模型，
     避免在 artex 里选到 gpt/claude/qwen 等走别的协议端点的模型。

用法：python3 go-shim.py [端口]   （默认 8399，容器内监听，无需发布端口）
artex 的 LLM BASE URL 填：http://127.0.0.1:8399
"""
import http.client
import json
import os
import socketserver
import ssl
import sys
import time
import uuid

import http.server

UPSTREAM = os.environ.get("GO_SHIM_UPSTREAM", "opencode.ai").strip() or "opencode.ai"
GLOBAL_MODELS = "/inference/go/v1/models"
SESSION = os.environ.get("GO_SHIM_SESSION") or str(uuid.uuid4())
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else int(os.environ.get("GO_SHIM_PORT", "8399"))
# 容器内 artex 与 shim 同容器，默认只听 loopback；确实要跨容器访问再设 GO_SHIM_BIND=0.0.0.0。
BIND = os.environ.get("GO_SHIM_BIND", "127.0.0.1").strip() or "127.0.0.1"
# 默认只记状态/模型/路径；排查 400/429 时设 GO_SHIM_DEBUG=1 才打印请求/响应正文。
DEBUG = os.environ.get("GO_SHIM_DEBUG", "") == "1"

# Go 端点上、走 openai chat/completions 协议的模型白名单。
# 依据官方文档的 Endpoints 表（https://opencode.ai/v2/docs/console/go/）：
# gpt-*/grok-*/muse-spark 走 /v1/responses，claude-*/qwen*/minimax-m3 走
# anthropic，gemini 走 google —— 这些 artex 的 openai 格式调不了，故不下发。
# beam-free 在中国大陆/港澳不可用（官方 RegionError），默认不下发，避免误选。
# 新模型上线后需人工补充到这里。
OPENAI_FAMILY_IDS = {
    "deepseek-v4-pro", "deepseek-v4.1-flash",
    "glm-5.1", "glm-5.2", "glm-5.3", "glm-5.3-flash",
    "hy3", "hy4-preview",
    "kimi-k2.6", "kimi-k2.7-code", "kimi-k3", "kimi-k3.1-pro",
    "longcat-2.0",
    "mimo-v2.5", "mimo-v2.5-pro", "mimo-v2.6-flash", "mimo-v2.6-pro",
    "minimax-m3.1-flash",
    "step-5-preview",
}

HOP_BY_HOP = {
    "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
    "te", "trailers", "transfer-encoding", "upgrade", "host",
    "content-length", "accept-encoding", "proxy-connection",
}

_models_cache = {"at": 0.0, "payload": None}


def log(msg):
    stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    sys.stderr.write("[go-shim] %s %s\n" % (stamp, msg))
    sys.stderr.flush()


def map_path(raw: str) -> str:
    """把客户端路径映射到 Go 的真实端点。"""
    path, _, query = raw.partition("?")
    # 已经填了完整 Go 路径时原样放行，避免二次拼接。
    if path.startswith("/inference/go/"):
        return raw
    if path.startswith("/v1/messages") or path == "/messages":
        rest = path[3:] if path.startswith("/v1") else path
        target = "/inference/go/anthropic/v1" + rest
    else:
        rest = path[3:] if path.startswith("/v1") else path
        if not rest.startswith("/"):
            rest = "/" + rest
        target = "/inference/go/openai/v1" + rest
    return target + (("?" + query) if query else "")


def build_models_payload():
    """拉全局模型列表并按协议族白名单过滤（缓存 10 分钟）。失败返回 None。"""
    now = time.time()
    if _models_cache["payload"] and now - _models_cache["at"] < 600:
        return _models_cache["payload"]
    conn = http.client.HTTPSConnection(UPSTREAM, 443, timeout=30,
                                       context=ssl.create_default_context())
    try:
        conn.request("GET", GLOBAL_MODELS, headers={"User-Agent": "artex-coding-agent/1.0"})
        resp = conn.getresponse()
        data = json.loads(resp.read(200_000))
        all_models = data.get("data", [])
        keep = [m for m in all_models if m.get("id") in OPENAI_FAMILY_IDS]
        total = len(all_models)
        data["data"] = sorted(keep, key=lambda m: m["id"])
        _models_cache.update(at=now, payload=data)
        log("models list: %d/%d served" % (len(keep), total))
        return data
    except Exception as exc:
        log("models fetch failed: %s" % exc)
        return None
    finally:
        conn.close()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass  # 访问日志关闭（任务量大时会刷屏），错误详情见 log()

    def _send_json(self, status: int, payload: dict):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True

    def _read_body(self) -> bytes:
        te = (self.headers.get("Transfer-Encoding") or "").lower()
        if "chunked" in te:
            chunks = []
            while True:
                size_line = self.rfile.readline().strip()
                if b";" in size_line:
                    size_line = size_line.split(b";")[0]
                size = int(size_line or b"0", 16)
                if size == 0:
                    self.rfile.readline()
                    break
                chunks.append(self.rfile.read(size))
                self.rfile.readline()
            return b"".join(chunks)
        length = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(length) if length else b""

    def _forward(self):
        path = self.path.split("?")[0].rstrip("/")
        if path == "/healthz":
            self._send_json(200, {"ok": True, "service": "go-shim"})
            return
        # 任何以 /models 结尾的请求都由本地回答（Go 的 openai 家族没有 models 接口）
        if path.endswith("/models"):
            payload = build_models_payload()
            if payload is None:
                self._send_json(502, {"error": "go-shim: models endpoint unavailable"})
                return
            self._send_json(200, payload)
            return

        body = self._read_body()
        target = map_path(self.path)

        conn = http.client.HTTPSConnection(
            UPSTREAM, 443, timeout=300,
            context=ssl.create_default_context(),
        )
        headers = {}
        for key, value in self.headers.items():
            if key.lower() in HOP_BY_HOP:
                continue
            headers[key] = value
        # 保留 artex 按会话发来的 x-opencode-session（每个 conversation 一个稳定 ID，
        # 利于 Go 的提示缓存/路由）；仅在客户端没带时用本进程 UUID 兜底。
        if not any(k.lower() == "x-opencode-session" for k in headers):
            headers["x-opencode-session"] = SESSION
        headers["User-Agent"] = "artex-coding-agent/1.0"  # Go 要求自报家门，别用通用 UA
        if body:
            headers["Content-Length"] = str(len(body))

        try:
            conn.request(self.command, target, body=body, headers=headers)
            resp = conn.getresponse()
        except Exception as exc:
            log("upstream error: %s" % exc)
            self._send_json(502, {"error": "go-shim: upstream error: %s" % exc})
            return

        # 非 2xx 时把请求/响应体打出来（artex 只报状态码，不报原因）。
        # 默认只记摘要，避免 429 风暴把整段 prompt 写进日志；GO_SHIM_DEBUG=1 才打正文。
        if resp.status >= 400:
            model_hint, auth_hint = "?", "no"
            try:
                model_hint = json.loads(body or b"{}").get("model", "?")
            except Exception:
                pass
            if any(k.lower() == "authorization" for k in self.headers):
                auth_hint = "yes"
            err = resp.read(800)
            detail = err[:300].decode("utf-8", "replace")
            if DEBUG:
                log("ERR %s %s model=%s auth=%s\n    req=%s\n    resp=%s" % (
                    resp.status, target, model_hint, auth_hint,
                    (body or b"")[:700].decode("utf-8", "replace"),
                    err[:500].decode("utf-8", "replace"),
                ))
            else:
                log("ERR %s %s model=%s auth=%s resp=%s" % (
                    resp.status, target, model_hint, auth_hint, detail,
                ))
            self.send_response(resp.status)
            ctype = dict(resp.getheaders()).get("Content-Type", "application/json")
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(err)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(err)
            conn.close()
            self.close_connection = True
            return

        self.send_response(resp.status)
        for key, value in resp.getheaders():
            if key.lower() in HOP_BY_HOP or key.lower() == "content-length":
                continue
            self.send_header(key, value)
        # 上游可能是流式（chunked），用关闭连接来界定响应结束
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            while True:
                block = resp.read(8192)
                if not block:
                    break
                self.wfile.write(block)
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            conn.close()
            self.close_connection = True

    do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = do_HEAD = _forward


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    log("listening on %s:%d -> https://%s (session=%s…)" % (BIND, PORT, UPSTREAM, SESSION[:8]))
    Server((BIND, PORT), Handler).serve_forever()
