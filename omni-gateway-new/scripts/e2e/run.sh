#!/bin/sh
# One-command end-to-end run: mock upstream + fixtures + three verdict matrices.
#
#   scripts/e2e/run.sh              start the mock upstream, provision, verify
#   scripts/e2e/run.sh --no-mock    reuse an already-running mock upstream
#
# Requires a gateway built from this tree and already listening (see README.md).
# Exits non-zero as soon as a verdict does not match.
set -eu

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=${PYTHON:-python3}
PORT=${E2E_MOCK_PORT:-19099}
LOG_DIR=${E2E_LOG_DIR:-${TMPDIR:-/tmp}}
MOCK_PID=""
USE_MOCK=1

for arg in "$@"; do
    case "$arg" in
        --no-mock) USE_MOCK=0 ;;
        -h|--help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "unknown argument: $arg" >&2; exit 2 ;;
    esac
done

cleanup() {
    if [ -n "$MOCK_PID" ]; then
        kill "$MOCK_PID" 2>/dev/null || true
        wait "$MOCK_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

if [ "$USE_MOCK" = 1 ]; then
    echo "== starting mock upstream on 127.0.0.1:$PORT"
    "$PYTHON" "$HERE/mock_upstream.py" "$PORT" >"$LOG_DIR/e2e-mock-upstream.log" 2>&1 &
    MOCK_PID=$!
    # Wait for the port to answer instead of guessing a sleep.
    ready=0
    for _ in $(seq 1 50); do
        if "$PYTHON" -c "
import socket, sys
s = socket.socket()
s.settimeout(0.2)
sys.exit(0 if s.connect_ex(('127.0.0.1', $PORT)) == 0 else 1)
" 2>/dev/null; then
            ready=1
            break
        fi
        sleep 0.2
    done
    if [ "$ready" != 1 ]; then
        echo "mock upstream did not come up; see $LOG_DIR/e2e-mock-upstream.log" >&2
        exit 1
    fi
fi

echo "== provisioning fixtures"
"$PYTHON" "$HERE/setup.py"

echo "== native protocol matrix"
"$PYTHON" "$HERE/native_matrix.py"

echo "== OpenAI-compatible chat fidelity matrix"
"$PYTHON" "$HERE/openai_compat_matrix.py"

echo "== policy interception matrix"
"$PYTHON" "$HERE/policy_matrix.py"

echo
echo "== e2e harness: all matrices passed"
