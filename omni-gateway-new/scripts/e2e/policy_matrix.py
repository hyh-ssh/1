"""Policy interception matrix: rate-limit (rpm / tpm / concurrency) and budget enforcement.

Self-provisioning: it deactivates every existing budget rule so that only the rule
under test can reject, then creates its own user, API key and budget rule for the
budget case -- so the run does not depend on leftovers from an earlier one.

Rate-limit windows are one minute wide, so the run waits for a fresh window first
(set E2E_FRESH_WAIT=0 to skip the wait when you know the window is clean).

Exit code is non-zero if any verdict differs from the expected one.
"""
import os
import threading
import time

import lib

FRESH_WAIT = int(os.environ.get("E2E_FRESH_WAIT", "62"))
FAILURES = []


def call(key, model):
    body = {"model": model, "messages": [{"role": "user", "content": "hi"}]}
    status, _, data = lib._open("POST", "/v1/chat/completions", body,
                                headers={"Authorization": "Bearer " + key})
    code = data.get("error", {}).get("code", "ok") if isinstance(data, dict) else "?"
    return status, code


def verify(label, results, expected, order_matters=True):
    codes = [status for status, _ in results]
    # Parallel calls finish in an arbitrary order, so only the multiset is fixed.
    ok = codes == expected if order_matters else sorted(codes) == sorted(expected)
    if not ok:
        FAILURES.append("%s: got %s want %s" % (label, codes, expected))
    print("== [%s] %s" % ("PASS" if ok else "FAIL", label))
    for status, code in results:
        print("     %s %s" % (status, code))
    print("     codes: %s (expected %s%s)" % (codes, expected, "" if order_matters else ", any order"))


def deactivate_budgets():
    """Remove budget interference: rpm/tpm/concurrency must be the only rejections."""
    status, _, items = lib.get("/api/admin/budgets")
    if status != 200:
        raise SystemExit("list budgets: %s %s" % (status, items))
    active = [item for item in items or [] if item.get("active")]
    for item in active:
        lib.patch("/api/admin/budgets/%s" % item["id"], {"active": False})
    print("deactivated %d active budget rule(s) of %d" % (len(active), len(items or [])))


def test_rpm():
    results = [call(lib.key("rpm"), "mock-rpm") for _ in range(3)]
    verify("RPM limit = 2 on one key, 3 sequential calls", results, [200, 200, 429])


def test_tpm():
    results = [call(lib.key("tpm"), "mock-rpm") for _ in range(2)]
    verify("TPM limit = 1 on one key, 2 sequential calls", results, [200, 429])


def test_concurrency():
    out = [None, None]
    barrier = threading.Barrier(2)

    def worker(slot):
        barrier.wait()
        out[slot] = call(lib.key("conc"), "mock-slow")

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(2)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    verify("Concurrency limit = 1 on one key, 2 parallel calls to a 2s upstream",
           out, [200, 429], order_matters=False)


def provision_budget_user():
    stamp = int(time.time())
    name = "e2e-budget-%d" % stamp
    status, _, user = lib.post("/api/admin/users", {"username": name, "display_name": name,
                                                    "role": "user", "password": "E2eUser!2345",
                                                    "active": True})
    if status not in (200, 201):
        raise SystemExit("create user: %s %s" % (status, user))
    status, _, created = lib.post("/api/admin/api-keys", {"user_id": user["id"], "name": name,
                                                          "model_allow": ["mock-budget"],
                                                          "active": True})
    if status not in (200, 201):
        raise SystemExit("create key: %s %s" % (status, created))
    # Amount fields travel as decimal strings, not JSON numbers. The two ledgers
    # are independent and have different units, so this rule is deliberately tight
    # on credits and loose on cost: it proves the credit ledger is what rejects.
    # MEASURED, not derived: an actual e2e harness run against a locally built
    # server plus the mock upstream produced exactly these figures, matching the
    # mock's fixed token counts and the PRICES rates in setup.py. An OpenAI chat
    # reply is prompt_tokens=11, completion_tokens=7; at 0.0002 credits per input
    # token and 0.0004 per output token, quantized per meter to 3 decimals, that is
    # 11*0.0002 = 0.0022 -> 0.002 plus 7*0.0004 = 0.0028 -> 0.003, i.e. 0.005
    # credits a call. The same usage at cost 0.000002/0.000004 CNY per token is
    # 11*0.000002 + 7*0.000004 = 0.00005 CNY. So the first call lands over 0.004
    # credits while staying far below the 1.00 CNY ceiling.
    budget = {"user_id": user["id"], "window_secs": 3600, "cost_limit": "1.00",
              "credit_limit": "0.004", "active": True}
    status, _, rule = lib.post("/api/admin/budgets", budget)
    if status not in (200, 201):
        raise SystemExit("create budget: %s %s" % (status, rule))
    return created["secret"], rule["id"]


def test_budget():
    secret, rule_id = provision_budget_user()
    results = [call(secret, "mock-budget") for _ in range(3)]
    verify("Budget credit_limit = 0.004 (cost_limit loose at 1.00), fresh user, 3 sequential calls",
           results, [200, 429, 429])

    lib.patch("/api/admin/budgets/%s" % rule_id, {"active": False})
    results = [call(secret, "mock-budget") for _ in range(2)]
    verify("Same budget key after the rule is deactivated, 2 sequential calls",
           results, [200, 200])


if __name__ == "__main__":
    lib.login()
    deactivate_budgets()
    if FRESH_WAIT:
        print("waiting %ds for a fresh per-minute rate-limit window ..." % FRESH_WAIT)
        time.sleep(FRESH_WAIT)
    test_rpm()
    test_tpm()
    test_concurrency()
    test_budget()
    if FAILURES:
        raise SystemExit("policy matrix FAILED:\n  " + "\n  ".join(FAILURES))
    print("policy matrix: all verdicts match")
