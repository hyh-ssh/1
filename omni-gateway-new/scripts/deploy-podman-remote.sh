#!/usr/bin/env bash
set -Eeuo pipefail

# Deploy the gateway to a remote Podman service without requiring a Docker daemon.
# The default target is the hermes-remote connection configured in podman.
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CONNECTION=${PODMAN_CONNECTION:-hermes-remote}
ENV_FILE=${ENV_FILE:-"${HOME}/.config/llm-gateway/${CONNECTION}.env"}
PUBLIC_URL=${PUBLIC_URL:-http://192.168.2.21:8080}
APP_IMAGE=${APP_IMAGE:-localhost/llm-gateway-backend:deploy}
POSTGRES_IMAGE=${POSTGRES_IMAGE:-quay.io/sclorg/postgresql-16-c9s:latest}
REMOTE_BASE_IMAGE=${REMOTE_BASE_IMAGE:-docker.io/library/ubuntu:24.04}
NETWORK_NAME=${NETWORK_NAME:-llm-gateway-net}
POSTGRES_VOLUME=${POSTGRES_VOLUME:-llm-gateway_postgres_data}
POSTGRES_CONTAINER=${POSTGRES_CONTAINER:-llm-gateway-postgres}
BACKEND_CONTAINER=${BACKEND_CONTAINER:-llm-gateway-backend}

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Environment file not found: $ENV_FILE" >&2
  echo "Copy .env.example, fill the secrets, and set ENV_FILE to that file." >&2
  exit 1
fi

if ! command -v podman >/dev/null 2>&1; then
  echo "podman is required on the client" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1 || ! command -v npm >/dev/null 2>&1; then
  echo "go and npm are required to build the Linux gateway image locally" >&2
  exit 1
fi

# Production only ever runs main: this script builds the image from the working
# tree, so a feature branch or a dirty tree would ship code that is not on main.
# shellcheck source=scripts/lib/deploy-guard.sh
. "$ROOT_DIR/scripts/lib/deploy-guard.sh"
require_main_branch

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${GATEWAY_MASTER_KEY:?GATEWAY_MASTER_KEY is required}"
: "${GATEWAY_ADMIN_USER:=admin}"
: "${GATEWAY_ADMIN_PASSWORD:?GATEWAY_ADMIN_PASSWORD is required}"
: "${POSTGRES_DB:=llm_gateway}"
: "${POSTGRES_USER:=gateway}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${GATEWAY_BUSINESS_TZ:=Asia/Shanghai}"
: "${GATEWAY_CONTENT_LOGGING:=false}"
: "${GATEWAY_CONTENT_LOG_MAX_BYTES:=1048576}"
: "${GATEWAY_COOKIE_SECURE:=false}"
: "${GATEWAY_SHUTDOWN_TIMEOUT:=10s}"
: "${GATEWAY_SESSION_TTL:=24h}"
: "${GATEWAY_LOG_RETENTION_DAYS:=0}"

podman --connection "$CONNECTION" info >/dev/null
REMOTE_ARCH=$(podman --connection "$CONNECTION" info --format '{{.Host.Arch}}')
case "$REMOTE_ARCH" in
  amd64|x86_64) GOARCH=amd64 ;;
  arm64|aarch64) GOARCH=arm64 ;;
  *) echo "Unsupported remote architecture: $REMOTE_ARCH" >&2; exit 1 ;;
esac

TEMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/llm-gateway-podman.XXXXXX")
cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

printf '%s\n' 'Building the Vue bundle locally...'
(cd "$ROOT_DIR/frontend" && npm ci && npm run build)

printf '%s\n' "Building a Linux/$GOARCH runtime image on the remote Podman host..."
mkdir -p "$TEMP_DIR/backend" "$TEMP_DIR/zoneinfo"
cp -R "$ROOT_DIR/backend/." "$TEMP_DIR/backend/"
rm -rf "$TEMP_DIR/backend/internal/httpapi/static"
mkdir -p "$TEMP_DIR/backend/internal/httpapi/static"
cp -R "$ROOT_DIR/frontend/dist/." "$TEMP_DIR/backend/internal/httpapi/static/"
(
  cd "$TEMP_DIR/backend"
  CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -ldflags='-s -w' -o "$TEMP_DIR/server" ./cmd/server
)
rm -rf "$TEMP_DIR/backend"

CA_CERT_FILE=${SSL_CERT_FILE:-}
if [[ -z "$CA_CERT_FILE" || ! -f "$CA_CERT_FILE" ]]; then
  for candidate in /etc/ssl/certs/ca-certificates.crt /etc/ssl/cert.pem /etc/pki/tls/certs/ca-bundle.crt; do
    if [[ -f "$candidate" ]]; then CA_CERT_FILE=$candidate; break; fi
  done
fi
if [[ -z "$CA_CERT_FILE" || ! -f "$CA_CERT_FILE" ]]; then
  echo "Could not find a CA bundle; set SSL_CERT_FILE before running this script." >&2
  exit 1
fi
cp "$CA_CERT_FILE" "$TEMP_DIR/ca-certificates.crt"

TZ_SOURCE="/usr/share/zoneinfo/$GATEWAY_BUSINESS_TZ"
if [[ ! -f "$TZ_SOURCE" ]]; then
  echo "Timezone data not found: $TZ_SOURCE" >&2
  echo "Set GATEWAY_BUSINESS_TZ to an installed zone or install tzdata on the client." >&2
  exit 1
fi
mkdir -p "$TEMP_DIR/zoneinfo/$(dirname "$GATEWAY_BUSINESS_TZ")"
cp "$TZ_SOURCE" "$TEMP_DIR/zoneinfo/$GATEWAY_BUSINESS_TZ"
if [[ -f /usr/share/zoneinfo/UTC ]]; then
  mkdir -p "$TEMP_DIR/zoneinfo"
  cp /usr/share/zoneinfo/UTC "$TEMP_DIR/zoneinfo/UTC"
fi

cat > "$TEMP_DIR/Containerfile" <<EOF
FROM $REMOTE_BASE_IMAGE
COPY server /usr/local/bin/server
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY zoneinfo/ /usr/share/zoneinfo/
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER nobody
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
EOF

# --pull=never makes a remote registry outage fail clearly instead of silently
# changing the base image used by an existing deployment.
if ! podman --connection "$CONNECTION" image exists "$POSTGRES_IMAGE"; then
  podman --connection "$CONNECTION" pull "$POSTGRES_IMAGE"
fi
podman --connection "$CONNECTION" build --pull=never --format docker -f "$TEMP_DIR/Containerfile" -t "$APP_IMAGE" "$TEMP_DIR"

podman --connection "$CONNECTION" network inspect "$NETWORK_NAME" >/dev/null 2>&1 || \
  podman --connection "$CONNECTION" network create "$NETWORK_NAME" >/dev/null
podman --connection "$CONNECTION" volume inspect "$POSTGRES_VOLUME" >/dev/null 2>&1 || \
  podman --connection "$CONNECTION" volume create "$POSTGRES_VOLUME" >/dev/null

# The remote host currently uses the Red Hat/SCL PostgreSQL image because
# Docker Hub is not reachable from it. That image uses POSTGRESQL_* variables
# and stores its data under /var/lib/pgsql/data.
podman --connection "$CONNECTION" rm -f "$BACKEND_CONTAINER" "$POSTGRES_CONTAINER" >/dev/null 2>&1 || true
podman --connection "$CONNECTION" run -d \
  --name "$POSTGRES_CONTAINER" \
  --network "$NETWORK_NAME" \
  --restart always \
  -e POSTGRESQL_DATABASE="$POSTGRES_DB" \
  -e POSTGRESQL_USER="$POSTGRES_USER" \
  -e POSTGRESQL_PASSWORD="$POSTGRES_PASSWORD" \
  -v "$POSTGRES_VOLUME:/var/lib/pgsql/data" \
  "$POSTGRES_IMAGE" >/dev/null

for _ in $(seq 1 90); do
  if podman --connection "$CONNECTION" exec "$POSTGRES_CONTAINER" pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
if ! podman --connection "$CONNECTION" exec "$POSTGRES_CONTAINER" pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; then
  echo 'PostgreSQL did not become ready' >&2
  podman --connection "$CONNECTION" logs --tail 100 "$POSTGRES_CONTAINER" >&2 || true
  exit 1
fi

DB_URL=${GATEWAY_DB_URL:-"postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${POSTGRES_CONTAINER}:5432/${POSTGRES_DB}?sslmode=disable"}
podman --connection "$CONNECTION" run -d \
  --name "$BACKEND_CONTAINER" \
  --network "$NETWORK_NAME" \
  --restart always \
  -p "${BACKEND_PORT:-8080}:8080" \
  -e GATEWAY_LISTEN_ADDR=:8080 \
  -e GATEWAY_DB_URL="$DB_URL" \
  -e GATEWAY_MASTER_KEY="$GATEWAY_MASTER_KEY" \
  -e GATEWAY_ADMIN_USER="$GATEWAY_ADMIN_USER" \
  -e GATEWAY_ADMIN_PASSWORD="$GATEWAY_ADMIN_PASSWORD" \
  -e GATEWAY_BUSINESS_TZ="$GATEWAY_BUSINESS_TZ" \
  -e GATEWAY_CONTENT_LOGGING="$GATEWAY_CONTENT_LOGGING" \
  -e GATEWAY_CONTENT_LOG_MAX_BYTES="$GATEWAY_CONTENT_LOG_MAX_BYTES" \
  -e GATEWAY_COOKIE_SECURE="$GATEWAY_COOKIE_SECURE" \
  -e GATEWAY_SHUTDOWN_TIMEOUT="$GATEWAY_SHUTDOWN_TIMEOUT" \
  -e GATEWAY_SESSION_TTL="$GATEWAY_SESSION_TTL" \
  -e GATEWAY_LOG_RETENTION_DAYS="$GATEWAY_LOG_RETENTION_DAYS" \
  "$APP_IMAGE" >/dev/null

for _ in $(seq 1 90); do
  if curl -fsS --max-time 3 "$PUBLIC_URL/readyz" >/dev/null 2>&1; then
    printf '%s\n' 'Remote gateway is ready.'
    break
  fi
  sleep 2
done
if ! curl -fsS --max-time 5 "$PUBLIC_URL/readyz"; then
  echo >&2
  echo 'Gateway did not become ready; recent logs:' >&2
  podman --connection "$CONNECTION" logs --tail 100 "$BACKEND_CONTAINER" >&2 || true
  exit 1
fi
printf '\n%s\n' 'Containers:'
podman --connection "$CONNECTION" ps --filter "name=$BACKEND_CONTAINER" --filter "name=$POSTGRES_CONTAINER" --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Ports}}'
