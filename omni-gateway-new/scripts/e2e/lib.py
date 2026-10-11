"""Shared client for the end-to-end harness.

Two surfaces are wrapped:

  * the admin API  -- session cookie + ``X-CSRF-Token`` (``get``/``post``/``patch``/``delete``)
  * the data plane -- a gateway API key (``chat`` and friends)

Connection settings come from the environment so the harness can point at any
instance:

    E2E_GATEWAY         base URL of the gateway        (default http://127.0.0.1:18090)
    E2E_ADMIN_USER      admin username                 (default admin)
    E2E_ADMIN_PASSWORD  admin password                 (default E2eAdmin!2345)

API keys are read from ``fixtures.json`` next to this file, which
``scripts/e2e/setup.py`` writes. Secrets are only ever returned once by the
gateway, so that file is the single place they are kept (and it is gitignored).
"""
import json
import os
import tempfile
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
FIXTURES_PATH = os.environ.get("E2E_FIXTURES", os.path.join(HERE, "fixtures.json"))

GW = os.environ.get("E2E_GATEWAY", "http://127.0.0.1:18090").rstrip("/")
ADMIN = os.environ.get("E2E_ADMIN_USER", "admin")
PASSWORD = os.environ.get("E2E_ADMIN_PASSWORD", "E2eAdmin!2345")

_cookie = None
_csrf = None
_fixtures = None


def fixtures():
    global _fixtures
    if _fixtures is None:
        if not os.path.exists(FIXTURES_PATH):
            raise SystemExit("no %s; run scripts/e2e/setup.py first" % FIXTURES_PATH)
        with open(FIXTURES_PATH) as handle:
            _fixtures = json.load(handle)
    return _fixtures


def key(name):
    """Return a data-plane API key recorded by setup.py (chat/rpm/conc/tpm)."""
    try:
        return fixtures()["keys"][name]
    except KeyError:
        raise SystemExit("fixture key %r is missing; re-run scripts/e2e/setup.py" % name)


def save_fixtures(data):
    """Atomically replace the secret-bearing fixture with a private file."""
    directory = os.path.dirname(os.path.abspath(FIXTURES_PATH))
    fd, temporary = tempfile.mkstemp(prefix=".e2e-fixtures-", dir=directory)
    try:
        with os.fdopen(fd, "w") as handle:
            json.dump(data, handle, indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(temporary, FIXTURES_PATH)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def _open(method, path, body=None, headers=None, raw=False):
    url = GW + path
    data = None
    hdrs = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        hdrs["Content-Type"] = "application/json"
    if headers:
        hdrs.update(headers)
    if _cookie:
        hdrs["Cookie"] = _cookie
    if _csrf:
        hdrs["X-CSRF-Token"] = _csrf
    request = urllib.request.Request(url, data=data, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(request) as response:
            text = response.read().decode()
            if raw:
                return response.status, dict(response.headers), text
            return response.status, dict(response.headers), (json.loads(text) if text else None)
    except urllib.error.HTTPError as err:
        text = err.read().decode()
        if raw:
            return err.code, dict(err.headers), text
        try:
            return err.code, dict(err.headers), (json.loads(text) if text else None)
        except ValueError:
            return err.code, dict(err.headers), text


def login(username=ADMIN, password=PASSWORD):
    """Open an admin session and remember the cookie plus CSRF token."""
    global _cookie, _csrf
    status, headers, data = _open("POST", "/api/auth/login",
                                  {"username": username, "password": password})
    if status != 200:
        raise SystemExit("login failed: %s %s" % (status, data))
    _cookie = headers.get("Set-Cookie", "").split(";")[0]
    _csrf = data["csrf_token"]
    return data


def get(path):
    return _open("GET", path)


def post(path, body):
    return _open("POST", path, body)


def patch(path, body):
    return _open("PATCH", path, body)


def delete(path):
    return _open("DELETE", path)


def gw(method, path, body=None, key_name="chat"):
    """Call the data plane with one of the harness API keys."""
    return _open(method, path, body, headers={"Authorization": "Bearer " + key(key_name)})
