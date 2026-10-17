#!/usr/bin/env bash
# Builds README.md's quick start (its first ```go block that is a whole
# program) in a scratch module that requires this module through a replace
# directive, so the README can't drift from the API it shows.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

awk '
  /^```go$/ { inblock = 1; code = ""; first = ""; next }
  /^```$/ && inblock { inblock = 0; if (first == "package main") { printf "%s", code; exit } next }
  inblock { if (first == "") first = $0; code = code $0 "\n" }
' "$root/README.md" >"$tmp/main.go"
[ -s "$tmp/main.go" ] || { echo "check-readme: no \`package main\` go block in README.md" >&2; exit 1; }

cd "$tmp"
go mod init example.com/quickstart >/dev/null 2>&1
go mod edit -replace "github.com/sinatra-dev/sinatra-go=$root"
go mod tidy >/dev/null 2>&1 || go mod tidy
go vet ./...
go build -o /dev/null .
echo "README quick start builds ($(wc -l <main.go | tr -d ' ') lines)"
