"""Provision the fixtures the end-to-end matrices need. Idempotent.

Creates, on the target instance:

  * two providers pointing at ``scripts/e2e/mock_upstream.py`` -- one
    OpenAI-compatible, one Anthropic -- so protocol routing can be exercised;
  * one alias per protocol and policy scenario, each with a single route;
  * two price versions of ``mock-gpt-4o``, the second one pricing an ``image``
    meter, plus a separate price for the Anthropic model;
  * an admin key and four user keys, and the rate-limit rules that bind three of
    them (rpm / concurrency / tpm).

API key secrets are written to ``scripts/e2e/fixtures.json`` (gitignored): the
gateway only returns a secret once, so that file is where the harness reads them
from. Re-running deletes and re-issues the harness keys, so the file always holds
live secrets. ``mock-unpriced`` deliberately has no price version -- it exists to
reproduce the ``unpriced`` settlement path.
"""
import json
import os

import lib

MOCK_PORT = int(os.environ.get("E2E_MOCK_PORT", "19099"))

OPENAI_PROVIDER = {"name": "mock-upstream", "type": "openai_compatible",
                   "base_url": "http://127.0.0.1:%d/v1" % MOCK_PORT,
                   "timeout_ms": 30000, "active": True, "supports_responses": True}
ANTHROPIC_PROVIDER = {"name": "mock-anthropic", "type": "anthropic",
                      "base_url": "http://127.0.0.1:%d" % MOCK_PORT,
                      "timeout_ms": 30000, "active": True}

# alias -> (provider name, upstream model). mock-gpt-4o-slow makes the mock sleep,
# which is what the concurrency case needs.
OPENAI_DEFAULT = "mock-gpt-4o"
ALIASES = [
    ("mock-chat", "mock-upstream", OPENAI_DEFAULT),
    ("mock-embed", "mock-upstream", OPENAI_DEFAULT),
    ("mock-image", "mock-upstream", OPENAI_DEFAULT),
    ("mock-responses", "mock-upstream", OPENAI_DEFAULT),
    ("mock-translate", "mock-upstream", OPENAI_DEFAULT),
    ("mock-slow", "mock-upstream", "mock-gpt-4o-slow"),
    ("mock-rpm", "mock-upstream", OPENAI_DEFAULT),
    ("mock-budget", "mock-upstream", OPENAI_DEFAULT),
    ("mock-unpriced", "mock-upstream", "mock-gpt-4o-mini"),
    ("mock-claude", "mock-anthropic", "mock-claude"),
    ("mock-unsupported", "mock-anthropic", "mock-claude"),
]
ALLOW = [name for name, _, _ in ALIASES]

# model -> (version, cost in CNY per unit, credits per unit). Two independent
# ledgers: ``cost`` is what the upstream provider charges us, ``credits`` is what
# the caller spends from their quota. A credit amount is quantized to three
# decimals, so the credit rates are picked to survive that rounding: the mock
# answers a chat call with 11 input and 7 output tokens, and the ledger quantizes
# each meter separately, giving 11 x 0.0002 = 0.0022 -> 0.002 credits for input
# and 7 x 0.0004 = 0.0028 -> 0.003 for output, i.e. exactly 0.005 credits a call.
PRICES = [
    ("mock-gpt-4o", 1, {"input": "0.000002", "output": "0.000004"},
     {"input": "0.0002", "output": "0.0004"}),
    ("mock-gpt-4o", 2, {"input": "0.000002", "output": "0.000004", "image": "0.02"},
     {"input": "0.0002", "output": "0.0004", "image": "2.000"}),
    ("mock-claude", 1, {"input": "0.000003", "output": "0.000006"},
     {"input": "0.0002", "output": "0.0004"}),
]

USER = {"username": "e2euser", "display_name": "E2E restricted user", "role": "user",
        "password": "E2eUser!2345", "active": True}

# key nickname -> (owner, rule). The rule is applied to the issued key id.
KEYS = [
    ("chat", "admin", None),
    ("rpm", "e2euser", {"scope": "key", "rpm": 2}),
    ("conc", "e2euser", {"scope": "key", "concurrency": 1}),
    ("tpm", "e2euser", {"scope": "key", "tpm": 1}),
]
KEY_NAMES = ["e2e-key-" + name for name, _, _ in KEYS]


def expect(status, data, what):
    if status not in (200, 201):
        raise SystemExit("%s failed: %s %s" % (what, status, json.dumps(data)[:400]))
    return data


def ensure_provider(name):
    payload = OPENAI_PROVIDER if name == "mock-upstream" else ANTHROPIC_PROVIDER
    _, _, items = lib.get("/api/admin/providers")
    for item in items or []:
        if item.get("name") == name:
            # 复用一个已存在的实例时必须对齐协议开关：0007 之前建的供应商行默认
            # supports_responses=false，而 mock 上游实现了 /v1/responses，不对齐
            # 会让矩阵里的 Responses 用例收到 501 responses_unsupported。
            want = bool(payload.get("supports_responses", False))
            if bool(item.get("supports_responses", False)) != want:
                status, _, updated = lib.patch("/api/admin/providers/%s" % item["id"],
                                                {"supports_responses": want})
                expect(status, updated, "update provider " + name)
                print("provider %-16s supports_responses -> %s" % (name, want))
            return item["id"]
    status, _, created = lib.post("/api/admin/providers", payload)
    expect(status, created, "create provider " + name)
    print("provider %-16s created id=%s" % (name, created["id"]))
    return created["id"]


def ensure_aliases(providers):
    _, _, items = lib.get("/api/admin/aliases")
    existing = {}
    for item in items or []:
        existing[item["name"]] = item["id"]
    aliases = {}
    for name, provider_name, model in ALIASES:
        if name in existing:
            aliases[name] = existing[name]
            continue
        status, _, created = lib.post("/api/admin/aliases", {"name": name})
        expect(status, created, "create alias " + name)
        alias_id = created["id"]
        status, _, route = lib.post("/api/admin/aliases/%d/routes" % alias_id,
                                    {"provider_id": providers[provider_name],
                                     "provider_model": model, "priority": 0})
        expect(status, route, "create route for " + name)
        aliases[name] = alias_id
        print("alias %-18s created id=%-3s -> %s/%s" % (name, alias_id, provider_name, model))
    return aliases


def ensure_prices():
    _, _, items = lib.get("/api/admin/prices")
    existing = set()
    for item in items or []:
        existing.add((item["model_id"], item["version"]))
    for model_id, version, cost, credits in PRICES:
        if (model_id, version) in existing:
            continue
        status, _, created = lib.post("/api/admin/prices", {"model_id": model_id,
                                                           "version": version,
                                                           "cost": cost, "credits": credits})
        expect(status, created, "create price %s v%s" % (model_id, version))
        print("price %-14s v%s created id=%s" % (model_id, version, created["id"]))


def ensure_user(username):
    _, _, users = lib.get("/api/admin/users")
    for user in users or []:
        if user["username"] == username:
            return user["id"]
    status, _, created = lib.post("/api/admin/users", USER)
    expect(status, created, "create user " + username)
    print("user %-16s created id=%s" % (username, created["id"]))
    return created["id"]


def reset_keys(user_ids):
    """Delete and re-issue the harness keys so fixtures.json holds live secrets."""
    _, _, keys = lib.get("/api/admin/api-keys")
    for entry in keys or []:
        if entry["name"] in KEY_NAMES:
            lib.delete("/api/admin/api-keys/%s" % entry["id"])
    secrets = {}
    ids = {}
    for name, owner, _ in KEYS:
        status, _, created = lib.post("/api/admin/api-keys",
                                      {"user_id": user_ids[owner], "name": "e2e-key-" + name,
                                       "model_allow": ALLOW, "active": True})
        expect(status, created, "create key " + name)
        secrets[name] = created["secret"]
        ids[name] = created["key"]["id"]
        print("key %-16s created id=%-3s (secret stored only in fixtures)" % (name, ids[name]))
    return secrets, ids


def reset_rate_limits(key_ids):
    """Rebuild the rules so every run starts from fresh in-memory counters."""
    _, _, rules = lib.get("/api/admin/rate-limits")
    for rule in rules or []:
        if rule["scope"] == "key" and rule["scope_id"] in key_ids.values():
            lib.delete("/api/admin/rate-limits/%s" % rule["id"])
    created = {}
    for name, _, rule in KEYS:
        if not rule:
            continue
        payload = dict(rule)
        payload["scope_id"] = key_ids[name]
        payload["active"] = True
        status, _, body = lib.post("/api/admin/rate-limits", payload)
        expect(status, body, "create rate-limit for " + name)
        created[name] = body["id"]
        print("rate-limit %-9s %s" % (name, json.dumps(body, sort_keys=True)))
    return created


def main():
    lib.login()
    providers = {name: ensure_provider(name) for name in ("mock-upstream", "mock-anthropic")}
    aliases = ensure_aliases(providers)
    ensure_prices()
    user_ids = {"admin": ensure_user("admin"), "e2euser": ensure_user(USER["username"])}
    secrets, key_ids = reset_keys(user_ids)
    rate_limits = reset_rate_limits(key_ids)
    lib.save_fixtures({"gateway": lib.GW, "providers": providers, "aliases": aliases,
                       "users": user_ids, "keys": secrets, "key_ids": key_ids,
                       "rate_limits": rate_limits})
    print("fixtures written to %s" % lib.FIXTURES_PATH)


if __name__ == "__main__":
    main()
