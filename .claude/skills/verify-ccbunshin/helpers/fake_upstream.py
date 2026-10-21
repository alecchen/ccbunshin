#!/usr/bin/env python3
"""Fake OpenAI chat-completions upstream, for driving the ccbunshin proxy.

Usage: python3 -I fake_upstream.py <port> <request-log>

Every request is appended to <request-log> as one JSON line, so a verification
run can prove what the proxy actually sent upstream. A request with
"stream": true gets OpenAI-style SSE chunks; anything else gets one buffered
JSON response. Usage reports 7 prompt tokens of which 2 were cache hits, which
the proxy must show as cache_read_input_tokens=2 and input_tokens=5.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
LOG = sys.argv[2]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _record(self, body):
        with open(LOG, "a") as fh:
            fh.write(json.dumps({"method": self.command, "path": self.path,
                                 "headers": dict(self.headers), "body": body}) + "\n")

    def _send(self, status, payload, content_type="application/json"):
        out = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("content-type", content_type)
        self.send_header("content-length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def do_GET(self):
        self._record(None)
        self._send(405, {"error": "this fake answers POST only"})

    def do_POST(self):
        length = int(self.headers.get("content-length") or 0)
        raw = self.rfile.read(length)
        try:
            body = json.loads(raw or b"{}")
        except ValueError:
            body = {"_unparsed": raw.decode("utf-8", "replace")}
        self._record(body)

        if not self.path.endswith("/chat/completions"):
            self._send(404, {"error": {"message": "unknown path " + self.path}})
            return

        model = body.get("model")
        if body.get("stream"):
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("cache-control", "no-cache")
            self.end_headers()
            chunks = [
                {"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": model,
                 "choices": [{"index": 0, "finish_reason": None,
                              "delta": {"role": "assistant", "content": "pong"}}]},
                {"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": model,
                 "choices": [{"index": 0, "finish_reason": "stop", "delta": {}}],
                 "usage": {"prompt_tokens": 7, "completion_tokens": 1, "total_tokens": 8,
                           "prompt_tokens_details": {"cached_tokens": 2}}},
            ]
            for chunk in chunks:
                self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
                self.wfile.flush()
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            self.close_connection = True
            return

        self._send(200, {
            "id": "chatcmpl-fake",
            "object": "chat.completion",
            "model": model,
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": "pong"}}],
            "usage": {"prompt_tokens": 7, "completion_tokens": 1, "total_tokens": 8,
                      "prompt_tokens_details": {"cached_tokens": 2}},
        })

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
