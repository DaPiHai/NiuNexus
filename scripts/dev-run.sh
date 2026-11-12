#!/usr/bin/env bash
# Local development runner. Starts the core as the current user (read-only
# netlink works without root) and the web UI on 127.0.0.1:8081.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${ROOT}/src"
DEV="${ROOT}/.dev"
BIN="${DEV}/bin"
RUN="${DEV}/run"
GO="${GO:-${ROOT}/.tools/go/bin/go}"
[ -x "${GO}" ] || GO="$(command -v go)"

export CGO_ENABLED=0 GOOS=linux GOFLAGS="-mod=mod"
mkdir -p "${BIN}" "${RUN}/etc" "${RUN}/var" "${RUN}/target"
rm -f "${RUN}/var/core.sock" "${RUN}/target/app.sock"

VERSION="$(awk -F= '/^version[[:space:]]*=/{gsub(/[[:space:]]/, "", $2); print $2; exit}' "${ROOT}/niunexus/manifest")"
LDFLAGS="-X niunexus/internal/version.Version=${VERSION:-dev} -X niunexus/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "==> building development binaries"
"${GO}" build -C "${SRC}" -trimpath -ldflags "${LDFLAGS}" -o "${BIN}/core" ./cmd/core
"${GO}" build -C "${SRC}" -trimpath -ldflags "${LDFLAGS}" -o "${BIN}/web" ./cmd/web
"${GO}" build -C "${SRC}" -trimpath -ldflags "${LDFLAGS}" -o "${BIN}/cli" ./cmd/cli

TOKEN="$(openssl rand -hex 32)"
export NIUNEXUS_RPC_TOKEN="${TOKEN}"
export NIUNEXUS_CONFIG_DIR="${RUN}/etc"
export NIUNEXUS_VAR_DIR="${RUN}/var"
export NIUNEXUS_CORE_SOCKET="${RUN}/var/core.sock"
export NIUNEXUS_APP_SOCKET="${RUN}/target/app.sock"
export NIUNEXUS_APP_UID="$(id -u)"
export NIUNEXUS_APP_GID="$(id -g)"

cleanup() {
  echo
  echo "==> stopping development processes"
  [ -n "${WEB_PID:-}" ] && kill "${WEB_PID}" 2>/dev/null || true
  [ -n "${CORE_PID:-}" ] && kill "${CORE_PID}" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "==> starting core"
"${BIN}/core" -socket "${NIUNEXUS_CORE_SOCKET}" >"${RUN}/core.log" 2>&1 &
CORE_PID=$!

for _ in $(seq 1 50); do
  [ -S "${NIUNEXUS_CORE_SOCKET}" ] && break
  sleep 0.1
done

echo "==> starting web"
NIUNEXUS_DEV=1 NIUNEXUS_DEV_LISTEN=127.0.0.1:8081 \
  "${BIN}/web" -socket "${NIUNEXUS_APP_SOCKET}" -listen 127.0.0.1:8081 -dev \
  >"${RUN}/web.log" 2>&1 &
WEB_PID=$!

sleep 0.4
echo
echo "NiuNexus development UI: http://127.0.0.1:8081/"
echo "Gateway-style URL:        http://127.0.0.1:8081/app/niunexus/"
echo "Core log:                 ${RUN}/core.log"
echo "Web log:                  ${RUN}/web.log"
echo
echo "Examples:"
echo "  curl -s http://127.0.0.1:8081/api/v1/session | jq"
echo "  curl -s -H 'X-Trim-Isadmin: true' http://127.0.0.1:8081/api/v1/overview | jq"
echo
echo "Press Ctrl-C to stop."
wait
