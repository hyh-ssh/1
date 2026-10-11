import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODELS = ["mock-gpt-4o", "mock-gpt-4o-mini"]
DEFAULT_MODEL = "mock-gpt-4o"
ANSWER = "\u8fd9\u662f\u6765\u81ea mock \u4e0a\u6e38\u7684\u56de\u590d\u3002"
COMPAT_CREATED = 1700000000
COMPAT_FINGERPRINT = "fp_compat_fixture"



class H(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            return json.loads(raw or b"{}")
        except Exception:
            return {}

    def do_GET(self):
        if self.path.startswith("/v1/models"):
            self._json(200, {"object": "list", "data": [
                {"id": m, "object": "model", "created": 0, "owned_by": "mock"}
                for m in MODELS
            ]})
            return
        self._json(404, {"error": {"message": "not found", "type": "invalid_request_error"}})

    def do_POST(self):
        req = self._read()
        model = req.get("model") or DEFAULT_MODEL
        if model.endswith("-slow"):
            time.sleep(2)
        if model == "broken-model":
            self._json(500, {"error": {"message": "upstream exploded"}})
            return
        if model == "unknown-model":
            self._json(404, {"error": {"message": "model not found"}})
            return
        path = self.path.split("?")[0]
        handler = {
            "/v1/chat/completions": self._chat,
            "/v1/embeddings": self._embeddings,
            "/v1/images/generations": self._images,
            "/v1/messages": self._messages,
            "/v1/responses": self._responses,
        }.get(path)
        if handler is None:
            self._json(404, {"error": {"message": "not found", "type": "invalid_request_error"}})
            return
        handler(model, req)

    def _chat(self, model, req):
        if req.get("user") == "compat-fixture":
            self._compat_chat(model, req)
            return

        if req.get("stream"):
            self._stream(model)
            return
        self._json(200, {
            "id": "chatcmpl-mock-1",
            "object": "chat.completion",
            "created": 1700000000,
            "model": model,
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": ANSWER},
                "finish_reason": "stop",
            }],
            "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
        })

    def _compat_chat(self, model, req):
        # A renamed token-limit key must fail at the actual HTTP upstream.
        if req.get("max_completion_tokens") != 32 or "max_tokens" in req:
            self._json(400, {"error": {"message": "max_completion_tokens required",
                                        "type": "invalid_request_error"}})
            return
        if req.get("stream"):
            self._compat_stream(model)
            return
        self._json(200, {
            "id": "chatcmpl-compat-nonstream", "object": "chat.completion",
            "created": COMPAT_CREATED, "model": model,
            "system_fingerprint": COMPAT_FINGERPRINT,
            "choices": [{"index": 0,
                         "message": {"role": "assistant", "content": "compat answer",
                                     "reasoning_content": "compat reasoning"},
                         "logprobs": {"content": [{"token": "ok", "logprob": -0.25,
                                                    "top_logprobs": []}]},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18,
                      "completion_tokens_details": {"reasoning_tokens": 3}},
        })

    def _compat_stream(self, model):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        base = {"id": "chatcmpl-compat-stream", "object": "chat.completion.chunk",
                "created": COMPAT_CREATED, "model": model,
                "system_fingerprint": COMPAT_FINGERPRINT}

        def choice(index, delta, finish=None):
            return {"index": index, "delta": delta, "finish_reason": finish}

        self._sse(dict(base, choices=[choice(0, {"role": "assistant"})]))
        self._sse(dict(base, choices=[choice(0, {"reasoning_content": "think "})]))
        self._sse(dict(base, choices=[choice(0, {"reasoning_content": "twice"})]))
        # Two parallel tool calls share choice 0; choice 1 shares this SSE event.
        self._sse(dict(base, choices=[
            choice(0, {"tool_calls": [
                {"index": 0, "id": "call_compat_0", "type": "function",
                 "function": {"name": "lookup", "arguments": "{\"id\":"}},
                {"index": 1, "id": "call_compat_1", "type": "function",
                 "function": {"name": "search", "arguments": "{\"q\":"}},
            ]}),
            choice(1, {"role": "assistant", "content": "alternate"}),
        ]))
        self._sse(dict(base, choices=[choice(0, {"tool_calls": [
            {"index": 1, "function": {"arguments": "\"x\"}"}},
            {"index": 0, "function": {"arguments": "1}"}},
        ]})]))
        self._sse(dict(base, choices=[choice(0, {}, "tool_calls")]))
        self._sse(dict(base, choices=[choice(1, {}, "stop")]))
        self._sse(dict(base, choices=[], usage={
            "prompt_tokens": 13, "completion_tokens": 9, "total_tokens": 22,
            "completion_tokens_details": {"reasoning_tokens": 4},
        }))
        self.wfile.write(b"data: [DONE]\n\n")

    def _stream(self, model):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        base = {"id": "chatcmpl-mock-stream", "object": "chat.completion.chunk",
                "created": 1700000000, "model": model}
        self._sse({"choices": [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}]})
        for piece in [ANSWER[i:i + 6] for i in range(0, len(ANSWER), 6)]:
            self._sse(dict(base, choices=[{"index": 0, "delta": {"content": piece}, "finish_reason": None}]))
        self._sse(dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": "stop"}]))
        self._sse(dict(base, choices=[], usage={"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}))
        self.wfile.write(("data: " + chr(91) + "DONE" + chr(93) + chr(10) + chr(10)).encode())

    def _sse(self, obj):
        self.wfile.write(("data: " + json.dumps(obj, ensure_ascii=False) + chr(10) + chr(10)).encode())
        self.wfile.flush()

    def _embeddings(self, model, req):
        self._json(200, {
            "object": "list",
            "model": model,
            "data": [{"object": "embedding", "index": 0, "embedding": [0.01, 0.02, 0.03]}],
            "usage": {"prompt_tokens": 8, "total_tokens": 8},
        })

    def _images(self, model, req):
        self._json(200, {
            "created": 1700000000,
            "model": model,
            "data": [{"url": "https://example.invalid/mock-image.png", "revised_prompt": "mock"}],
            "usage": {"input_tokens": 4, "output_tokens": 0, "total_tokens": 4},
        })

    def _messages(self, model, req):
        self._json(200, {
            "id": "msg_mock",
            "type": "message",
            "role": "assistant",
            "model": model,
            "content": [{"type": "text", "text": ANSWER}],
            "stop_reason": "end_turn",
            "stop_sequence": None,
            "usage": {"input_tokens": 9, "output_tokens": 5},
        })

    def _responses(self, model, req):
        self._json(200, {
            "id": "resp_mock",
            "object": "response",
            "created_at": 1700000000,
            "status": "completed",
            "model": model,
            "output": [{
                "type": "message",
                "id": "msg_resp",
                "role": "assistant",
                "content": [{"type": "output_text", "text": ANSWER, "annotations": []}],
            }],
            "usage": {"input_tokens": 10, "output_tokens": 6, "total_tokens": 16},
        })


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 19099
    print("mock upstream on 127.0.0.1:%d" % port, flush=True)
    ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
