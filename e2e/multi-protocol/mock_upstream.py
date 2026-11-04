#!/usr/bin/env python3
"""Mock upstream for the wings-api multi-protocol acceptance run (#30).

Serves OpenAI chat/completions (+ SSE), OpenAI responses, Anthropic messages and
Gemini generateContent on one port. Model name decides error behaviour:
mock-429 -> HTTP 429, mock-500 -> HTTP 500.
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

USAGE = {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}


def read_body(handler):
    length = int(handler.headers.get("Content-Length") or 0)
    return handler.rfile.read(length) if length else b"{}"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def _json(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _sse(self, chunks):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        for chunk in chunks:
            self.wfile.write(chunk.encode())
            self.wfile.flush()
            time.sleep(0.01)

    def do_POST(self):
        body = read_body(self)
        try:
            payload = json.loads(body or b"{}")
        except Exception:
            payload = {}
        model = str(payload.get("model") or "")
        if model == "mock-429":
            return self._json(429, {"error": {"message": "upstream rate limited", "type": "rate_limit_error"}})
        if model == "mock-500":
            return self._json(500, {"error": {"message": "upstream exploded", "type": "server_error"}})

        path = self.path.split("?")[0]
        if path.endswith("/chat/completions"):
            return self._chat(payload)
        if path.endswith("/responses"):
            return self._responses(payload)
        if path.endswith("/messages"):
            return self._anthropic(payload)
        if ":generateContent" in path or ":streamGenerateContent" in path:
            return self._gemini(payload)
        return self._json(404, {"error": {"message": f"no mock for {path}"}})

    def _chat(self, payload):
        if payload.get("tools"):
            tool_calls = [{"id": "call_mock", "type": "function",
                           "function": {"name": "get_weather", "arguments": "{\"city\":\"Shenzhen\"}"}}]
            if payload.get("stream"):
                return self._sse([
                    'data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock-gpt","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":' + json.dumps(tool_calls) + '},"finish_reason":"tool_calls"}]}\n\n',
                    "data: [DONE]\n\n",
                ])
            return self._json(200, {"id": "chatcmpl-mock", "object": "chat.completion",
                                    "model": "mock-gpt", "usage": USAGE,
                                    "choices": [{"index": 0, "message": {"role": "assistant", "content": None, "tool_calls": tool_calls}, "finish_reason": "tool_calls"}]})
        if payload.get("stream"):
            return self._sse([
                'data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock-gpt","choices":[{"index":0,"delta":{"role":"assistant","content":"po"}}]}\n\n',
                'data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock-gpt","choices":[{"index":0,"delta":{"content":"ng"},"finish_reason":"stop"}],"usage":' + json.dumps(USAGE) + '}\n\n',
                "data: [DONE]\n\n",
            ])
        return self._json(200, {"id": "chatcmpl-mock", "object": "chat.completion", "model": "mock-gpt",
                                "usage": USAGE,
                                "choices": [{"index": 0, "message": {"role": "assistant", "content": "pong"}, "finish_reason": "stop"}]})

    def _responses(self, payload):
        return self._json(200, {"id": "resp_mock", "object": "response", "created_at": int(time.time()),
                                "status": "completed", "model": "mock-gpt",
                                "output": [{"type": "message", "id": "msg_mock", "role": "assistant",
                                            "status": "completed",
                                            "content": [{"type": "output_text", "text": "pong", "annotations": []}]}],
                                "usage": {"input_tokens": 5, "output_tokens": 2, "total_tokens": 7}})

    def _anthropic(self, payload):
        return self._json(200, {"id": "msg_mock", "type": "message", "role": "assistant",
                                "model": "mock-claude",
                                "content": [{"type": "text", "text": "pong"}],
                                "stop_reason": "end_turn", "stop_sequence": None,
                                "usage": {"input_tokens": 5, "output_tokens": 2}})

    def _gemini(self, payload):
        return self._json(200, {"candidates": [{"content": {"parts": [{"text": "pong"}], "role": "model"},
                                                "finishReason": "STOP", "index": 0}],
                                "usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 2, "totalTokenCount": 7}})


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 19090
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
