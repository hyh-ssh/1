#!/usr/bin/env bash
set -Eeuo pipefail

# Reproducible verification gate for the LLM gateway.
#
# Runs the checks that must pass before every commit:
#   gofmt -> go vet -> go build -> go test -race -> frontend type-check + build + unit tests
#
# PostgreSQL integration tests (internal/store) need TEST_DATABASE_URL.
# Without it those tests self-skip and store coverage collapses; this script
# prints a loud warning instead of failing silently. Pass --strict (or set
# CI=true) to turn the missing variable into a hard failure.
#
# Usage:
#   scripts/test.sh                       # local, warn on missing DB URL
#   TEST_DATABASE_URL=... scripts/test.sh # full run
#   scripts/test.sh --strict              # fail if TEST_DATABASE_URL is unset

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND_DIR="${ROOT_DIR}/backend"
FRONTEND_DIR="${ROOT_DIR}/frontend"

STRICT=0
for arg in "$@"; do
  case "$arg" in
    --strict) STRICT=1 ;;
    -h|--help)
      sed -n '2,22p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

if [[ "${CI:-}" == "true" ]]; then
  STRICT=1
fi

step() {
  printf '\n\033[1;34m==> %s\033[0m\n' "$1"
}

fail() {
  printf '\033[1;31mFAILED: %s\033[0m\n' "$1" >&2
  exit 1
}

step "gofmt (backend)"
UNFORMATTED=$(cd "$BACKEND_DIR" && gofmt -l .)
if [[ -n "$UNFORMATTED" ]]; then
  printf '%s\n' "$UNFORMATTED" >&2
  fail "gofmt found unformatted files"
fi
echo "ok"

step "go vet (backend)"
(cd "$BACKEND_DIR" && go vet -mod=readonly ./...)
echo "ok"

step "go build (backend)"
(cd "$BACKEND_DIR" && go build -mod=readonly ./...)
echo "ok"

if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  printf '\033[1;33mWARNING: TEST_DATABASE_URL is not set.\033[0m\n' >&2
  printf '\033[1;33m  internal/store integration tests will self-skip and coverage drops.\033[0m\n' >&2
  printf '\033[1;33m  Re-run with TEST_DATABASE_URL=postgres://user@host:5432/db?sslmode=disable\033[0m\n' >&2
  if [[ "$STRICT" == "1" ]]; then
    fail "TEST_DATABASE_URL is required in strict mode"
  fi
else
  step "PostgreSQL integration database configured"
  echo "TEST_DATABASE_URL set; store tests will run."
fi

step "go test -race (backend)"
(cd "$BACKEND_DIR" && go test -mod=readonly -race -count=1 ./...)

step "frontend install + type-check + build + unit tests"
(cd "$FRONTEND_DIR" && npm ci && npm run build && npm run test)

printf '\n\033[1;32mAll checks passed.\033[0m\n'
