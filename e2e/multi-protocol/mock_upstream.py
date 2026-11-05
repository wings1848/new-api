#!/usr/bin/env python3
"""Mock upstream for the wings-api multi-protocol acceptance run (#30).

Serves OpenAI chat/completions (+ SSE), OpenAI responses, Anthropic messages and
Gemini generateContent on one port. Model name decides error behaviour:
mock-429 -> HTTP 429, mock-500 -> HTTP 500.
"""
import json
import os
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
        self.send_header("Connection", "close")
        self.close_connection = True
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
        if model == "mock-quota":
            return self._json(429, {"error": {"message": "You exceeded your current quota, please check your plan and billing details.",
                                              "type": "insufficient_quota", "code": "insufficient_quota"}})
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
        has_tool_result = any(
            isinstance(message, dict) and message.get("role") == "tool"
            for message in payload.get("messages") or []
        )
        if payload.get("tools") and not has_tool_result:
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
        if payload.get("stream"):
            response = {"id": "resp_mock", "object": "response", "created_at": int(time.time()),
                        "status": "in_progress", "model": "mock-gpt", "output": []}
            events = [
                ("response.created", {"type": "response.created", "response": response}),
                ("response.output_item.added", {"type": "response.output_item.added", "output_index": 0,
                                                "item": {"id": "msg_mock", "type": "message", "role": "assistant",
                                                         "status": "in_progress", "content": []}}),
                ("response.content_part.added", {"type": "response.content_part.added", "item_id": "msg_mock",
                                                 "output_index": 0, "content_index": 0,
                                                 "part": {"type": "output_text", "text": "", "annotations": []}}),
                ("response.output_text.delta", {"type": "response.output_text.delta", "item_id": "msg_mock",
                                                "output_index": 0, "content_index": 0, "delta": "po"}),
                ("response.output_text.delta", {"type": "response.output_text.delta", "item_id": "msg_mock",
                                                "output_index": 0, "content_index": 0, "delta": "ng"}),
                ("response.output_text.done", {"type": "response.output_text.done", "item_id": "msg_mock",
                                               "output_index": 0, "content_index": 0, "text": "pong"}),
                ("response.completed", {"type": "response.completed", "response": {
                    "id": "resp_mock", "object": "response", "created_at": int(time.time()),
                    "status": "completed", "model": "mock-gpt",
                    "output": [{"id": "msg_mock", "type": "message", "role": "assistant", "status": "completed",
                                "content": [{"type": "output_text", "text": "pong", "annotations": []}]}],
                    "usage": {"input_tokens": 5, "output_tokens": 2, "total_tokens": 7}}}),
            ]
            return self._sse([f"event: {name}\ndata: {json.dumps(data)}\n\n" for name, data in events])
        return self._json(200, {"id": "resp_mock", "object": "response", "created_at": int(time.time()),
                                "status": "completed", "model": "mock-gpt",
                                "output": [{"type": "message", "id": "msg_mock", "role": "assistant",
                                            "status": "completed",
                                            "content": [{"type": "output_text", "text": "pong", "annotations": []}]}],
                                "usage": {"input_tokens": 5, "output_tokens": 2, "total_tokens": 7}})

    def _anthropic(self, payload):
        if payload.get("stream"):
            events = [
                ("message_start", {"type": "message_start", "message": {
                    "id": "msg_mock", "type": "message", "role": "assistant", "model": "mock-claude",
                    "content": [], "stop_reason": None, "stop_sequence": None,
                    "usage": {"input_tokens": 5, "output_tokens": 1}}}),
                ("content_block_start", {"type": "content_block_start", "index": 0,
                                         "content_block": {"type": "text", "text": ""}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                         "delta": {"type": "text_delta", "text": "po"}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                         "delta": {"type": "text_delta", "text": "ng"}}),
                ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None},
                                   "usage": {"output_tokens": 2}}),
                ("message_stop", {"type": "message_stop"}),
            ]
            return self._sse([f"event: {name}\ndata: {json.dumps(data)}\n\n" for name, data in events])
        return self._json(200, {"id": "msg_mock", "type": "message", "role": "assistant",
                                "model": "mock-claude",
                                "content": [{"type": "text", "text": "pong"}],
                                "stop_reason": "end_turn", "stop_sequence": None,
                                "usage": {"input_tokens": 5, "output_tokens": 2}})

    def _gemini(self, payload):
        if "streamGenerateContent" in self.path.split("?")[0]:
            chunks = [
                {"candidates": [{"content": {"parts": [{"text": "po"}], "role": "model"}, "index": 0}]},
                {"candidates": [{"content": {"parts": [{"text": "ng"}], "role": "model"},
                                  "finishReason": "STOP", "index": 0}],
                 "usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 2, "totalTokenCount": 7}},
            ]
            return self._sse([f"data: {json.dumps(chunk)}\n\n" for chunk in chunks])
        return self._json(200, {"candidates": [{"content": {"parts": [{"text": "pong"}], "role": "model"},
                                                "finishReason": "STOP", "index": 0}],
                                "usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 2, "totalTokenCount": 7}})


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 19090
    host = os.environ.get("MOCK_HOST", "127.0.0.1")
    ThreadingHTTPServer((host, port), Handler).serve_forever()
