"""Native-protocol data-plane matrix: every /v1/* surface against both provider types.

Verdicts come from the gateway contract:

  * an OpenAI-compatible provider serves /v1/embeddings, /v1/images/generations
    and /v1/responses;
  * an Anthropic provider serves /v1/messages natively (passthrough);
  * /v1/messages routed to an OpenAI provider is translated to chat/completions;
  * /v1/embeddings routed to an Anthropic provider has no equivalent, so the
    gateway must answer 501 operation_unsupported instead of guessing.

Exit code is non-zero if any verdict differs from the expected one.
"""
import json
import sys

import lib

FAILURES = []

CASES = [
    ("POST /v1/embeddings on an OpenAI provider", "/v1/embeddings",
     {"model": "mock-embed", "input": "hello world"}, 200),
    ("POST /v1/images/generations on an OpenAI provider", "/v1/images/generations",
     {"model": "mock-image", "prompt": "a red cube"}, 200),
    ("POST /v1/responses on an OpenAI provider", "/v1/responses",
     {"model": "mock-responses", "input": "ping"}, 200),
    ("POST /v1/messages on an Anthropic provider (native passthrough)", "/v1/messages",
     {"model": "mock-claude", "max_tokens": 16,
      "messages": [{"role": "user", "content": "ping"}]}, 200),
    ("POST /v1/messages on an OpenAI provider (translated)", "/v1/messages",
     {"model": "mock-translate", "max_tokens": 16,
      "messages": [{"role": "user", "content": "ping"}]}, 200),
    ("POST /v1/embeddings on an Anthropic provider (unsupported)", "/v1/embeddings",
     {"model": "mock-unsupported", "input": "hello"}, 501),
]


def brief(data):
    if not isinstance(data, dict):
        return repr(data)[:160]
    if "error" in data:
        return "error=%s/%s" % (data["error"].get("code"), data["error"].get("type"))
    head = {name: data[name] for name in ("object", "model", "id") if name in data}
    if data.get("usage"):
        head["usage"] = data["usage"]
    if isinstance(data.get("data"), list):
        head["data_len"] = len(data["data"])
    return json.dumps(head, ensure_ascii=False)[:200]


def main():
    lib.login()
    for label, path, body, expected in CASES:
        status, _, data = lib.gw("POST", path, body)
        ok = status == expected
        if not ok:
            FAILURES.append("%s: got %s want %s" % (label, status, expected))
        print("== [%s] %s -> %s (expected %s)"
              % ("PASS" if ok else "FAIL", label, status, expected))
        print("     %s" % brief(data))
    if FAILURES:
        raise SystemExit("native protocol matrix FAILED:\n  " + "\n  ".join(FAILURES))
    print("native protocol matrix: all verdicts match")


if __name__ == "__main__":
    sys.exit(main())
