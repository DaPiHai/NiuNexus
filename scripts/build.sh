#!/usr/bin/env bash
# Build the Go binaries, generate icons, and package the FPK.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${ROOT}/src"
OUT="${ROOT}/niunexus/app/server"

GO="${GO:-${ROOT}/.tools/go/bin/go}"
if [ ! -x "${GO}" ]; then
  GO="$(command -v go || true)"
fi
[ -n "${GO}" ] || { echo "Go toolchain not found. Run Phase 0 setup first." >&2; exit 1; }

FNPACK="${FNPACK:-$(command -v fnpack || true)}"
[ -n "${FNPACK}" ] || { echo "fnpack not found in PATH" >&2; exit 1; }

VERSION="$(awk -F= '/^version[[:space:]]*=/{gsub(/[[:space:]]/, "", $2); print $2; exit}' "${ROOT}/niunexus/manifest")"
VERSION="${VERSION:-0.1.0}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
GOARCH="${GOARCH:-amd64}"

export CGO_ENABLED=0
export GOOS=linux
export GOARCH
export GOFLAGS="-mod=mod"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

LDFLAGS="-s -w -X niunexus/internal/version.Version=${VERSION} -X niunexus/internal/version.BuildTime=${BUILD_TIME}"

mkdir -p "${OUT}" "${ROOT}/niunexus/app/ui/images"

echo "==> generating icons"
(
  cd "${SRC}"
  "${GO}" run ./cmd/genicons -out "${ROOT}/niunexus"
)

echo "==> building niunexus-core (linux/${GOARCH})"
"${GO}" build -C "${SRC}" -trimpath -ldflags "${LDFLAGS}" -o "${OUT}/niunexus-core" ./cmd/core

echo "==> building niunexus-web"
"${GO}" build -C "${SRC}" -trimpath -ldflags "${LDFLAGS}" -o "${OUT}/niunexus-web" ./cmd/web

echo "==> building niunexus-cli"
"${GO}" build -C "${SRC}" -trimpath -ldflags "${LDFLAGS}" -o "${OUT}/niunexus-cli" ./cmd/cli

chmod +x "${OUT}/niunexus-core" "${OUT}/niunexus-web" "${OUT}/niunexus-cli"
chmod +x "${ROOT}"/niunexus/cmd/* 2>/dev/null || true
find "${ROOT}/niunexus" -name '.DS_Store' -delete 2>/dev/null || true

echo "==> packaging FPK"
(
  cd "${ROOT}"
  "${FNPACK}" build --directory niunexus
)

echo "==> done"
find "${ROOT}" -maxdepth 2 -name '*.fpk' -printf '%p %k KiB\n' 2>/dev/null || true
