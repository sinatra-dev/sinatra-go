#!/usr/bin/env bash
# Runs the contract tests (contract_test.go, build tag "contract"): starts
# `prism mock openapi.json --errors` on a free port, waits for it, runs the
# tests against it, then stops it. Needs Node.js 24 or later for Prism.
set -euo pipefail
cd "$(dirname "$0")/.."

PRISM_VERSION=5.16.0

port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
url="http://127.0.0.1:$port"

npx -y "@stoplight/prism-cli@$PRISM_VERSION" mock openapi.json --errors -h 127.0.0.1 -p "$port" -v warn &
prism=$!
trap 'kill "$prism" 2>/dev/null || true; wait "$prism" 2>/dev/null || true' EXIT

# The first run downloads Prism, so allow a few minutes.
for _ in $(seq 1 600); do
  if ! kill -0 "$prism" 2>/dev/null; then
    echo "contract: prism exited before it was ready" >&2
    exit 1
  fi
  if curl -s -o /dev/null -H 'Authorization: Bearer probe' "$url/v1/me"; then
    break
  fi
  sleep 0.5
done
curl -s -o /dev/null -H 'Authorization: Bearer probe' "$url/v1/me" ||
  { echo "contract: prism didn't start within 5 minutes" >&2; exit 1; }

SINATRA_CONTRACT_URL=$url go test -count=1 -tags contract -run '^TestContract' -v "$@" .
