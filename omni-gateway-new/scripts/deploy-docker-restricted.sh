#!/usr/bin/env bash
set -Eeuo pipefail

# Deploy the gateway to a Docker host that cannot reach a container registry or
# a package proxy. See docs/DEPLOY_172.md for the concrete host this targets and
# for the capability probe that showed it is unreachable.
#
# On such a host the multi-stage Dockerfile cannot build (no npm registry, no Go
# module proxy from inside build containers) and no image can be pulled, so this
# script:
#
#   1. builds the Vue console and cross-compiles a static linux binary on this
#      client, where the network works;
#   2. streams the two base images from a reachable engine into the remote
#      engine, but only when the remote does not already have them;
#   3. uploads the bundle and runs `docker compose up -d --build`, so the target
#      assembles the runtime image from deploy/docker-restricted/Dockerfile.runtime.
#
# Idempotent: the environment file already deployed on the host is adopted when
# present, so a re-run never mints a second master key or database password, and
# the PostgreSQL named volume survives every recreate. Generating secrets for a
# host that already has that volume is refused unless FORCE_NEW_SECRETS=1.
#
# Usage:
#   CONTAINER_CONNECTION=podman-machine-default scripts/deploy-docker-restricted.sh
#
# Recognised overrides: HOST, PUBLIC_URL, REMOTE_DIR, STACK_ENV, PLATFORM,
# CONTAINER_CLIENT, CONTAINER_CONNECTION, SSH, SCP, FORCE_NEW_SECRETS.

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
ASSETS_DIR=$ROOT_DIR/deploy/docker-restricted

HOST=${HOST:-root@172.20.80.130}
PUBLIC_URL=${PUBLIC_URL:-http://172.20.80.130:8080}
REMOTE_DIR=${REMOTE_DIR:-/opt/omni-gateway}
STACK_ENV=${STACK_ENV:-"$HOME/.config/llm-gateway/omni-gateway-${HOST#*@}.env"}
PLATFORM=${PLATFORM:-linux/amd64}
CONTAINER_CLIENT=${CONTAINER_CLIENT:-podman}
CONTAINER_CONNECTION=${CONTAINER_CONNECTION:-}
SSH=${SSH:-ssh}
SCP=${SCP:-scp}
SSH_OPTS=${SSH_OPTS:--o BatchMode=yes -o ConnectTimeout=15}

# Base images are transferred over SSH, so they must exist on the client for the
# remote platform. Keep these in sync with Dockerfile and docker-compose.yml.
ALPINE_IMAGE=${ALPINE_IMAGE:-alpine:3.20}
POSTGRES_IMAGE=${POSTGRES_IMAGE:-postgres:16-alpine}

ENGINE=("$CONTAINER_CLIENT")
if [[ -n "$CONTAINER_CONNECTION" ]]; then
  ENGINE+=(--connection "$CONTAINER_CONNECTION")
fi

log() { printf '%s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

for tool in go npm curl openssl "$CONTAINER_CLIENT" "$SSH" "$SCP"; do
  command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done
[[ -f "$ASSETS_DIR/Dockerfile.runtime" ]] || die "missing $ASSETS_DIR/Dockerfile.runtime"
[[ -f "$ASSETS_DIR/docker-compose.override.yml" ]] || die "missing $ASSETS_DIR/docker-compose.override.yml"

# Production only ever runs main: both deploy paths compile from the working
# tree, so a feature branch or a dirty tree would ship code that is not on main.
# shellcheck source=scripts/lib/deploy-guard.sh
. "$ROOT_DIR/scripts/lib/deploy-guard.sh"
require_main_branch

# ---------------------------------------------------------- target platform ---
REMOTE_ARCH=$($SSH $SSH_OPTS "$HOST" "docker info --format '{{.Architecture}}'") || die "cannot reach docker on $HOST"
case "$REMOTE_ARCH" in
  x86_64|amd64) REMOTE_GOARCH=amd64 ;;
  aarch64|arm64) REMOTE_GOARCH=arm64 ;;
  *) die "unsupported remote architecture: $REMOTE_ARCH" ;;
esac
case "$PLATFORM" in
  linux/amd64) CLIENT_GOARCH=amd64 ;;
  linux/arm64) CLIENT_GOARCH=arm64 ;;
  *) die "unsupported PLATFORM: $PLATFORM" ;;
esac
[[ "$REMOTE_GOARCH" = "$CLIENT_GOARCH" ]] || die "PLATFORM=$PLATFORM does not match the $REMOTE_ARCH remote host"
log "target $HOST: architecture $REMOTE_ARCH, platform $PLATFORM"

if $SSH $SSH_OPTS "$HOST" "ss -ltn 2>/dev/null | grep -q ':8080 '" && \
   ! $SSH $SSH_OPTS "$HOST" "cd $REMOTE_DIR 2>/dev/null && docker compose ps --services --filter status=running 2>/dev/null | grep -q backend"; then
  log "warning: port 8080 on $HOST is already bound by something other than this stack."
  log "         Stop the previous deployment first (for example: systemctl disable --now omni-gateway)."
fi

# -------------------------------------------------------- scratch workspace ---
TEMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/omni-gateway-docker.XXXXXX")
trap 'rm -rf "$TEMP_DIR"' EXIT

# ------------------------------------------------------------------- secrets ---
# The environment file already on the host is authoritative for a running stack:
# it holds the key that encrypts stored provider credentials and the password
# PostgreSQL was initialised with. Minting fresh secrets here would strand both,
# so an existing deployment is adopted rather than replaced.
ENV_DIR=$(dirname "$STACK_ENV")
mkdir -p "$ENV_DIR"
chmod 700 "$ENV_DIR"
umask 077

if $SSH $SSH_OPTS "$HOST" "test -f $REMOTE_DIR/.env"; then
  $SSH $SSH_OPTS "$HOST" "cat $REMOTE_DIR/.env" > "$TEMP_DIR/deployed.env"
  if [[ -f "$STACK_ENV" ]] && cmp -s "$STACK_ENV" "$TEMP_DIR/deployed.env"; then
    log "reusing the environment file at $STACK_ENV"
  else
    if [[ -f "$STACK_ENV" ]]; then
      cp -p "$STACK_ENV" "$STACK_ENV.bak"
      log "warning: $STACK_ENV differs from the deployed $HOST:$REMOTE_DIR/.env"
      log "         adopting the deployed file; the previous copy is at $STACK_ENV.bak"
    else
      log "adopting the environment file already deployed at $HOST:$REMOTE_DIR/.env"
    fi
    mv "$TEMP_DIR/deployed.env" "$STACK_ENV"
  fi
elif [[ -f "$STACK_ENV" ]]; then
  log "reusing the environment file at $STACK_ENV"
else
  PROJECT_NAME=$(basename "$REMOTE_DIR")
  if [[ -z "${FORCE_NEW_SECRETS:-}" ]] &&
     $SSH $SSH_OPTS "$HOST" "docker volume inspect ${PROJECT_NAME}_postgres_data >/dev/null 2>&1"; then
    die "the host has volume ${PROJECT_NAME}_postgres_data but no $REMOTE_DIR/.env
     A new master key and password would strand that database. Restore the
     environment file, or delete the volume and re-run with FORCE_NEW_SECRETS=1."
  fi
  log "generating a new environment file at $STACK_ENV"
  MASTER_KEY=$(openssl rand -hex 32)
  ADMIN_PASSWORD=$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')
  POSTGRES_PASSWORD=$(openssl rand -hex 24)
  cat > "$STACK_ENV" <<EOF
# omni-gateway deployment for $HOST -- keep this file out of the repository.
# Losing GATEWAY_MASTER_KEY makes encrypted provider credentials and content
# logs unrecoverable; keep a protected backup.
POSTGRES_DB="llm_gateway"
POSTGRES_USER="gateway"
POSTGRES_PASSWORD="$POSTGRES_PASSWORD"

GATEWAY_MASTER_KEY="$MASTER_KEY"
GATEWAY_ADMIN_USER="admin"
GATEWAY_ADMIN_PASSWORD="$ADMIN_PASSWORD"
GATEWAY_BUSINESS_TZ="Asia/Shanghai"
GATEWAY_CONTENT_LOGGING="false"
GATEWAY_CONTENT_LOG_MAX_BYTES="1048576"
GATEWAY_COOKIE_SECURE="false"
GATEWAY_SESSION_TTL="24h"
GATEWAY_SHUTDOWN_TIMEOUT="10s"
GATEWAY_LOG_RETENTION_DAYS="0"
GATEWAY_ALLOW_PRIVATE_UPSTREAMS="false"
EOF
fi
chmod 600 "$STACK_ENV"
grep -q '^GATEWAY_MASTER_KEY=' "$STACK_ENV" || die "GATEWAY_MASTER_KEY is missing from $STACK_ENV"
grep -q '^POSTGRES_PASSWORD=' "$STACK_ENV" || die "POSTGRES_PASSWORD is missing from $STACK_ENV"

# Compose derives the database URL from POSTGRES_* inside the stack's network, so
# the host-local GATEWAY_DB_URL of a native deployment must not leak into it.
grep -vE '^(GATEWAY_DB_URL|GATEWAY_LISTEN_ADDR)=' "$STACK_ENV" > "$TEMP_DIR/.env"
chmod 600 "$TEMP_DIR/.env"

# --------------------------------------------------------------------- build ---
log "building the Vue console..."
(cd "$ROOT_DIR/frontend" && npm ci && npm run build)

log "cross-compiling a static linux/$CLIENT_GOARCH binary..."
mkdir -p "$TEMP_DIR/backend"
cp -R "$ROOT_DIR/backend/." "$TEMP_DIR/backend/"
rm -f "$TEMP_DIR/backend/server"
rm -rf "$TEMP_DIR/backend/internal/httpapi/static"
mkdir -p "$TEMP_DIR/backend/internal/httpapi/static"
cp -R "$ROOT_DIR/frontend/dist/." "$TEMP_DIR/backend/internal/httpapi/static/"
(
  cd "$TEMP_DIR/backend"
  CGO_ENABLED=0 GOOS=linux GOARCH="$CLIENT_GOARCH" \
    go build -trimpath -ldflags='-s -w' -o "$TEMP_DIR/server" ./cmd/server
)
rm -rf "$TEMP_DIR/backend"
log "  binary: $(du -h "$TEMP_DIR/server" | cut -f1)"

# ------------------------------------------------------------- base images ---
for image in "$ALPINE_IMAGE" "$POSTGRES_IMAGE"; do
  if $SSH $SSH_OPTS "$HOST" "docker image inspect $image >/dev/null 2>&1"; then
    log "base image already on the host: $image"
    continue
  fi
  log "transferring $image from the client..."
  if ! "${ENGINE[@]}" image exists "$image" >/dev/null 2>&1; then
    "${ENGINE[@]}" pull --platform "$PLATFORM" "$image"
  fi
  # Compress on the wire; `docker load` reads the decompressed stream remotely.
  "${ENGINE[@]}" save --format docker-archive "$image" | gzip > "$TEMP_DIR/image.tar.gz"
  $SSH $SSH_OPTS "$HOST" 'gunzip | docker load' < "$TEMP_DIR/image.tar.gz"
  rm -f "$TEMP_DIR/image.tar.gz"
done

# -------------------------------------------------------------------- upload ---
log "uploading the deployment bundle to $REMOTE_DIR..."
$SSH $SSH_OPTS "$HOST" "install -d -m 0755 $REMOTE_DIR"
$SCP $SSH_OPTS -q "$ROOT_DIR/docker-compose.yml"                      "$HOST:$REMOTE_DIR/docker-compose.yml"
$SCP $SSH_OPTS -q "$ASSETS_DIR/Dockerfile.runtime"                   "$HOST:$REMOTE_DIR/Dockerfile.runtime"
$SCP $SSH_OPTS -q "$ASSETS_DIR/docker-compose.override.yml"          "$HOST:$REMOTE_DIR/docker-compose.override.yml"
$SCP $SSH_OPTS -q "$TEMP_DIR/server"                                 "$HOST:$REMOTE_DIR/server"
$SCP $SSH_OPTS -q "$TEMP_DIR/.env"                                   "$HOST:$REMOTE_DIR/.env"

$SSH $SSH_OPTS "$HOST" bash -s <<REMOTE
set -Eeuo pipefail
cd "$REMOTE_DIR"
chmod 0644 docker-compose.yml Dockerfile.runtime docker-compose.override.yml
chmod 0755 server
chmod 0600 .env
# alpine ships neither a CA bundle nor a timezone database, and apk cannot reach
# its CDN from here, so take both from the host itself.
cp /etc/ssl/certs/ca-certificates.crt ca-certificates.crt
mkdir -p zoneinfo/Asia
cp /usr/share/zoneinfo/Asia/Shanghai zoneinfo/Asia/Shanghai
cp /usr/share/zoneinfo/UTC zoneinfo/UTC
REMOTE

# --------------------------------------------------------------------- start ---
log "starting the stack..."
$SSH $SSH_OPTS "$HOST" "cd $REMOTE_DIR && docker compose up -d --build" >&2

log "waiting for $PUBLIC_URL/readyz ..."
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "$PUBLIC_URL/readyz" >/dev/null 2>&1; then break; fi
  sleep 2
done
if ! curl -fsS --max-time 5 "$PUBLIC_URL/readyz"; then
  log ""
  log "the gateway did not become ready; recent logs:"
  $SSH $SSH_OPTS "$HOST" "cd $REMOTE_DIR && docker compose logs --tail 80" >&2 || true
  exit 1
fi
log ""
$SSH $SSH_OPTS "$HOST" "cd $REMOTE_DIR && docker compose ps"
