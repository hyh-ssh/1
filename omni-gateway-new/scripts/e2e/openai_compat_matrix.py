"""Real HTTP OpenAI-compatible chat fidelity checks, using setup.py's mock-chat route.

The mock's user=compat-fixture branch rejects a renamed max_completion_tokens
field, so a 200 response also verifies the request reached the upstream intact.
Never print returned bodies, prompts or API keys.
"""
import json

import lib

PATH = "/v1/chat/completions"
ALIAS = "mock-chat"
CREATED = 1700000000
FINGERPRINT = "fp_compat_fixture"


def expect(label, condition):
    if not condition:
        raise SystemExit("openai compatibility matrix FAILED: " + label)


def request_body(stream):
    return {"model": ALIAS, "messages": [{"role": "user", "content": "compat probe"}],
            "user": "compat-fixture", "max_completion_tokens": 32, "stream": stream}


def nonstream():
    status, _, data = lib.gw("POST", PATH, request_body(False))
    expect("non-stream HTTP 200 (upstream token-limit key)", status == 200)
    expect("non-stream envelope", all(data.get(k) == v for k, v in {
        "id": "chatcmpl-compat-nonstream", "object": "chat.completion",
        "model": ALIAS, "created": CREATED, "system_fingerprint": FINGERPRINT,
    }.items()))
    expect("non-stream choice count", len(data["choices"]) == 1)
    choice = data["choices"][0]
    expect("non-stream choice metadata", choice["index"] == 0 and
           choice["finish_reason"] == "stop")
    message = choice["message"]
    expect("non-stream answer and reasoning", message["role"] == "assistant" and
           message["content"] == "compat answer" and
           message["reasoning_content"] == "compat reasoning")
    expect("non-stream logprobs", choice["logprobs"] == {
        "content": [{"token": "ok", "logprob": -0.25, "top_logprobs": []}],
    })
    expect("non-stream usage", data["usage"] == {
        "prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18,
        "completion_tokens_details": {"reasoning_tokens": 3},
    })
    print("== [PASS] OpenAI-compatible non-stream chat fidelity and alias")


def sse_data(text):
    frames = []
    pending = []
    for line in text.splitlines():
        if line == "":
            if pending:
                frames.append("\n".join(pending))
                pending = []
        elif line.startswith("data:"):
            value = line[5:]
            pending.append(value[1:] if value.startswith(" ") else value)
        elif not line.startswith(":"):
            raise SystemExit("openai compatibility matrix FAILED: invalid SSE framing")
    if pending:
        frames.append("\n".join(pending))
    return frames


def stream():
    # lib.gw returns parsed JSON; use the same authenticated data-plane client
    # with raw=True to inspect SSE frames rather than treating them as JSON.
    status, headers, text = lib._open(
        "POST", PATH, request_body(True),
        headers={"Authorization": "Bearer " + lib.key("chat")}, raw=True)
    expect("stream HTTP 200 (upstream token-limit key)", status == 200)
    expect("stream content type", headers.get("Content-Type", "").lower().startswith("text/event-stream"))
    frames = sse_data(text)
    expect("one terminal [DONE]", frames.count("[DONE]") == 1 and frames[-1] == "[DONE]")
    try:
        chunks = [json.loads(frame) for frame in frames[:-1]]
    except ValueError:
        raise SystemExit("openai compatibility matrix FAILED: invalid SSE JSON") from None
    expect("stream chunk count", len(chunks) == 9)
    expect("stream upstream identity, timestamp and alias", all(
        all(chunk.get(k) == v for k, v in {
            "id": "chatcmpl-compat-stream", "object": "chat.completion.chunk",
            "model": ALIAS, "created": CREATED, "system_fingerprint": FINGERPRINT,
        }.items()) for chunk in chunks))
    expect("stream choice count", all(len(chunk["choices"]) == 1 for chunk in chunks[:8])
           and chunks[8]["choices"] == [])
    choices = [chunk["choices"][0] for chunk in chunks[:8]]
    expect("stream choice indexes", [c["index"] for c in choices] ==
           [0, 0, 0, 0, 1, 0, 0, 1])
    expect("stream finish reasons", [c["finish_reason"] for c in choices] ==
           [None, None, None, None, None, None, "tool_calls", "stop"])
    expect("stream assistant and reasoning fragments", choices[0]["delta"]["role"] == "assistant"
           and [c["delta"]["reasoning_content"] for c in choices[1:3]] ==
           ["think ", "twice"])
    expect("stream second choice", choices[4]["delta"]["content"] == "alternate")

    heads = choices[3]["delta"]["tool_calls"]
    tails = choices[5]["delta"]["tool_calls"]
    expect("stream parallel tool heads", [(c["index"], c["id"], c["type"],
           c["function"]["name"], c["function"]["arguments"]) for c in heads] == [
               (0, "call_compat_0", "function", "lookup", '{"id":'),
               (1, "call_compat_1", "function", "search", '{"q":'),
           ])
    expect("stream interleaved tool arguments", [(c["index"], c["function"]["arguments"])
           for c in tails] == [(1, '"x"}'), (0, '1}')])
    expect("stream reasoning-token usage", chunks[8]["usage"] == {
        "prompt_tokens": 13, "completion_tokens": 9, "total_tokens": 22,
        "completion_tokens_details": {"reasoning_tokens": 4},
    })
    print("== [PASS] OpenAI-compatible SSE chunks, usage, DONE and alias")


def main():
    try:
        nonstream()
        stream()
    except (AttributeError, IndexError, KeyError, TypeError):
        raise SystemExit("openai compatibility matrix FAILED: unexpected response shape") from None
    print("openai compatibility matrix: all verdicts match")


if __name__ == "__main__":
    main()
