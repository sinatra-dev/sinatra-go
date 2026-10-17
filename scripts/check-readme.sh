#!/usr/bin/env bash
# Checks README.md's quick start: its code block (the first ```go block under
# "## Quick start") must be exactly the body of main in
# examples/quickstart/main.go, one tab less indented. Then builds that program
# in a scratch module that requires this module through a replace directive,
# the way a user's module would.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
example=$root/examples/quickstart/main.go

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

awk '
  /^## Quick start$/ { section = 1; next }
  section && /^```go$/ { inblock = 1; next }
  inblock && /^```$/ { exit }
  inblock { print }
' "$root/README.md" >"$tmp/readme.txt"
[ -s "$tmp/readme.txt" ] || { echo "check-readme: no go block under '## Quick start' in README.md" >&2; exit 1; }

awk '
  /^func main\(\) \{$/ { inmain = 1; next }
  inmain && /^\}$/ { exit }
  inmain { sub(/^\t/, ""); print }
' "$example" >"$tmp/example.txt"

if ! diff -u "$tmp/example.txt" "$tmp/readme.txt" >"$tmp/diff.txt"; then
  echo "check-readme: README.md's quick start isn't the body of main in examples/quickstart/main.go:" >&2
  cat "$tmp/diff.txt" >&2
  exit 1
fi

mkdir "$tmp/mod"
cp "$example" "$tmp/mod/main.go"
cd "$tmp/mod"
go mod init example.com/quickstart >/dev/null 2>&1
go mod edit -replace "github.com/sinatra-dev/sinatra-go=$root"
go mod tidy >/dev/null 2>&1 || go mod tidy
go vet ./...
go build -o /dev/null .
echo "README quick start matches examples/quickstart and builds ($(wc -l <"$tmp/readme.txt" | tr -d ' ') lines)"
