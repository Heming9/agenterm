#!/usr/bin/env python3
"""Ephemeral single-session SDP mailbox; never accepts file payloads.

Bind to loopback and put HTTPS in front for cross-network experiments.
This is a test fixture, not an Agenterm production signaling service.
"""
import argparse
import hmac
import json
import secrets
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def make_server(config, port=0, ttl=900):
    mailbox = {}
    lock = threading.Lock()
    expires = time.monotonic() + ttl

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass  # Do not log tokens, room names or SDP.

        def reply(self, status, body=b""):
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def authorize(self):
            if time.monotonic() >= expires:
                self.reply(410)
                return None
            if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + config["token"]):
                self.reply(401)
                return None
            for kind in ("offer", "answer"):
                if self.path == f'/v1/{config["session"]}/{kind}':
                    return kind
            self.reply(404)
            return None

        def do_GET(self):
            kind = self.authorize()
            if kind is None:
                return
            with lock:
                body = mailbox.get(kind)
            self.reply(200 if body is not None else 204, body or b"")

        def do_PUT(self):
            kind = self.authorize()
            if kind is None:
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if not 1 <= length <= 128 * 1024 or self.headers.get("Transfer-Encoding"):
                    raise ValueError("invalid length")
                self.connection.settimeout(5)
                value = json.loads(self.rfile.read(length))
                if set(value) != {"type", "sdp"} or value["type"] != kind or not isinstance(value["sdp"], str):
                    raise ValueError("invalid envelope")
                if not value["sdp"].startswith("v=0") or "a=candidate:" not in value["sdp"]:
                    raise ValueError("not SDP")
                body = json.dumps(value, separators=(",", ":")).encode()
            except (ValueError, TypeError, TimeoutError):
                self.reply(400)
                return
            with lock:
                previous = mailbox.get(kind)
                if previous is not None and previous != body:
                    self.reply(409)
                    return
                mailbox[kind] = body
            self.reply(200 if previous is not None else 201)

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.daemon_threads = True
    return server


def write_config(path, config):
    with Path(path).open("x", encoding="utf-8") as file:
        Path(path).chmod(0o600)
        json.dump(config, file, indent=2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--port", type=int, default=18765)
    parser.add_argument("--url", help="Create a fresh config with this public HTTPS URL")
    parser.add_argument("--ttl", type=int, default=900)
    args = parser.parse_args()
    if args.url:
        write_config(args.config, dict(signal_url=args.url, session=secrets.token_hex(16), token=secrets.token_hex(32)))
    config = json.loads(Path(args.config).read_text(encoding="utf-8"))
    server = make_server(config, args.port, args.ttl)
    print(json.dumps(dict(event="listening", port=server.server_port, ttl_seconds=args.ttl)), flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
