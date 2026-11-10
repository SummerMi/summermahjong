#!/usr/bin/env python3
"""web/ 的极小静态服务器 + `/api` 同源反代（**兜底方案**，零依赖、无构建）。

为什么需要它：
    `riichi-server` 默认带 `-cors "*"`，此时用 `file://` 直接打开 `index.html`
    也能 fetch 状态接口（浏览器跨源但服务端允许）。只有在你用 `-cors ""` 关掉 CORS、
    或者后端被换成了不带 CORS 的版本时，才需要这个脚本：它把静态页与 `/api` 放在
    同一个源上，浏览器就不再跨源。

用法：
    python web/serve.py                          # 8098 -> 127.0.0.1:8099
    python web/serve.py --port 8098 --target http://127.0.0.1:8099

然后浏览器打开：http://127.0.0.1:8098/

注意：WebSocket 不经过本脚本（直连 `服务地址` 的 ws://），因为服务端
`CheckOrigin` 允许所有来源，不需要反代。

只用 Python 3 标准库，不需要 pip install。
"""

from __future__ import annotations

import argparse
import http.server
import json
import os
import socketserver
import sys
import urllib.error
import urllib.request
from urllib.parse import urlsplit

WEB_DIR = os.path.dirname(os.path.abspath(__file__))

# 这些前缀走反代，其余请求按静态文件返回。
PROXY_PREFIXES = ("/api/", "/metrics", "/healthz")

# 转发给后端的请求头（Authorization 是座位令牌，必须原样带过去）。
FORWARD_HEADERS = ("Authorization", "Content-Type", "Accept", "If-None-Match")


class Handler(http.server.SimpleHTTPRequestHandler):
    """静态文件 + 反代。"""

    target = "http://127.0.0.1:8099"

    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=WEB_DIR, **kwargs)

    # ── 日志：只打印反代请求，静态文件不刷屏 ────────────────────
    def log_message(self, fmt, *args):
        if self.path.startswith(PROXY_PREFIXES):
            sys.stderr.write("[proxy] %s - %s\n" % (self.address_string(), fmt % args))

    # ── 反代 ───────────────────────────────────────────────────
    def _proxy(self, method: str) -> None:
        url = self.target.rstrip("/") + self.path
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length > 0 else None

        request = urllib.request.Request(url, data=body, method=method)
        for name in FORWARD_HEADERS:
            value = self.headers.get(name)
            if value:
                request.add_header(name, value)

        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                self._relay(response.status, response.headers, response.read())
        except urllib.error.HTTPError as exc:
            self._relay(exc.code, exc.headers, exc.read())
        except Exception as exc:  # 连接失败 / 超时 → 502，页面会显示到横幅里
            payload = json.dumps(
                {"error": {"code": "PROXY_FAILED", "message": str(exc)}},
                ensure_ascii=False,
            ).encode("utf-8")
            self.send_response(502)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

    def _relay(self, status: int, headers, data: bytes) -> None:
        self.send_response(status)
        for name, value in headers.items():
            if name.lower() in ("transfer-encoding", "connection", "content-length"):
                continue
            self.send_header(name, value)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Access-Control-Allow-Origin", "*")  # 同源其实用不到，给调试留个保险
        self.end_headers()
        if data:
            self.wfile.write(data)

    # ── 路由 ───────────────────────────────────────────────────
    def do_GET(self):  # noqa: N802 - http.server 约定
        if self.path.startswith(PROXY_PREFIXES):
            self._proxy("GET")
        else:
            super().do_GET()

    def do_POST(self):  # noqa: N802
        if self.path.startswith(PROXY_PREFIXES):
            self._proxy("POST")
        else:
            self.send_error(405, "只有 /api 前缀支持 POST")

    def do_OPTIONS(self):  # noqa: N802
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Authorization, Content-Type")
        self.end_headers()


class ThreadingServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def main() -> int:
    parser = argparse.ArgumentParser(description="静态页 + /api 同源反代（S11 前端兜底方案）")
    parser.add_argument("--port", type=int, default=8098, help="本地监听端口（默认 8098）")
    parser.add_argument(
        "--target",
        default="http://127.0.0.1:8099",
        help="riichi-server 地址（默认 http://127.0.0.1:8099）",
    )
    args = parser.parse_args()

    parsed = urlsplit(args.target)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        print("--target 必须是 http(s)://host:port，收到: %r" % args.target, file=sys.stderr)
        return 2

    Handler.target = args.target
    httpd = ThreadingServer(("127.0.0.1", args.port), Handler)

    print("=" * 66)
    print("  S11 最小牌桌 · 本地服务（静态 + /api 同源反代）")
    print("  页面    : http://127.0.0.1:%d/" % args.port)
    print("  后端    : %s" % args.target)
    print("  提示    : 页面里的「服务器」填 %s（WebSocket 直连，不经反代）" % args.target)
    print("  Ctrl+C  : 停止")
    print("=" * 66)

    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n已停止。")
    finally:
        httpd.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
