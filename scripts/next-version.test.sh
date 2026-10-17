#!/usr/bin/env bash
# Tests scripts/next-version.sh against fixture specs built on openapi.json (or
# the spec given). Shared byte for byte by sinatra-node, sinatra-python and
# sinatra-go, like next-version.sh.
# Needs oasdiff (or OASDIFF) and jq, like the script itself.
#
#   bash scripts/next-version.test.sh [spec.json]
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script="$here/next-version.sh"
base_spec=${1:-"$here/../openapi.json"}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The fixtures change only things this test injects, under names the API can
# never use (a /__test_only__/ path, testOnly.* operation ids, xTestOnly*
# fields), so they keep meaning the same thing whatever the real spec gains
# or loses. The real spec only provides a valid document to inject into.
if jq -e '.paths // {} | keys | any(startswith("/__test_only__"))' "$base_spec" >/dev/null; then
  echo "FAIL $base_spec already has a /__test_only__ path"
  exit 1
fi
op='.paths["/__test_only__/items"].get'
schema="$op.responses[\"200\"].content[\"application/json\"].schema"
jq '.paths["/__test_only__/items"] = {get: {
      operationId: "testOnly.list",
      summary: "Test only",
      description: "Injected by next-version.test.sh.",
      responses: {"200": {description: "OK", content: {"application/json": {schema: {
        type: "object",
        description: "Test only.",
        properties: {xTestOnlyRequired: {type: "string", description: "Test only."}},
        required: ["xTestOnlyRequired"]
      }}}}}
    }}' "$base_spec" >"$work/base.json"
# Prose only: oasdiff's changelog is empty.
jq "$op.description = \"Other words.\" | $schema.description = \"Other words.\" |
    $schema.properties.xTestOnlyRequired.description = \"Other words.\"" "$work/base.json" >"$work/prose.json"
# Additive: a new optional response field, and a new operation.
jq "$schema.properties.xTestOnlyField = {type: \"string\"}" "$work/base.json" >"$work/new-field.json"
jq '.paths["/__test_only__/items/{id}"] = {delete: {
      operationId: "testOnly.delete",
      parameters: [{name: "id", in: "path", required: true, schema: {type: "string"}}],
      responses: {"204": {description: "Deleted"}}
    }}' "$work/base.json" >"$work/new-operation.json"
# Breaking: the injected operation removed, and its required response field removed.
jq 'del(.paths["/__test_only__/items"])' "$work/base.json" >"$work/removed-operation.json"
jq "del($schema.properties.xTestOnlyRequired) | $schema.required = []" "$work/base.json" >"$work/removed-field.json"
# Not a spec at all.
printf '{not json' >"$work/broken.json"

failures=0
pass() { echo "ok   $1"; }
fail() {
  echo "FAIL $1"
  failures=$((failures + 1))
}

# expect <name> <expected stdout> <args...>
expect() {
  local name=$1 expected=$2 actual status
  shift 2
  set +e
  actual=$(bash "$script" "$@" 2>/dev/null)
  status=$?
  set -e
  if [ "$status" -eq 0 ] && [ "$actual" = "$expected" ]; then
    pass "$name -> $expected"
  else
    fail "$name: expected '$expected' (exit 0), got '$actual' (exit $status)"
  fi
}

# expect_error <name> <args...>: non-zero exit and nothing on stdout.
expect_error() {
  local name=$1 actual status
  shift
  set +e
  actual=$(bash "$script" "$@" 2>/dev/null)
  status=$?
  set -e
  if [ "$status" -ne 0 ] && [ -z "$actual" ]; then
    pass "$name -> exit $status"
  else
    fail "$name: expected a failure with no output, got '$actual' (exit $status)"
  fi
}

w=$work
expect "first release, tag argument empty" 0.1.0 "$w/base.json" "$w/base.json" ""
expect "first release, tag argument absent" 0.1.0 "$w/base.json" "$w/base.json"
expect "first release ignores a breaking diff" 0.1.0 "$w/base.json" "$w/removed-operation.json" ""

expect "0.x identical spec is a patch" 0.1.1 "$w/base.json" "$w/base.json" v0.1.0
expect "0.x prose-only change is a patch" 0.1.1 "$w/base.json" "$w/prose.json" v0.1.0
expect "0.x new field is a patch" 0.3.5 "$w/base.json" "$w/new-field.json" v0.3.4
expect "0.x new operation is a patch" 0.3.5 "$w/base.json" "$w/new-operation.json" v0.3.4
expect "0.x removed operation is a minor" 0.4.0 "$w/base.json" "$w/removed-operation.json" v0.3.4
expect "0.x removed field is a minor" 0.4.0 "$w/base.json" "$w/removed-field.json" v0.3.4
expect "0.x tag without v" 0.4.0 "$w/base.json" "$w/removed-field.json" 0.3.4

expect "1.x identical spec is a patch" 1.2.4 "$w/base.json" "$w/base.json" v1.2.3
expect "1.x prose-only change is a patch" 1.2.4 "$w/base.json" "$w/prose.json" v1.2.3
expect "1.x new field is a minor" 1.3.0 "$w/base.json" "$w/new-field.json" v1.2.3
expect "1.x new operation is a minor" 1.3.0 "$w/base.json" "$w/new-operation.json" v1.2.3
expect "1.x removed operation is a major" 2.0.0 "$w/base.json" "$w/removed-operation.json" v1.2.3
expect "1.x removed field is a major" 2.0.0 "$w/base.json" "$w/removed-field.json" v1.2.3
expect "multi-digit parts" 10.0.0 "$w/base.json" "$w/removed-field.json" v9.19.29

expect_error "tag that isn't vX.Y.Z" "$w/base.json" "$w/base.json" release-7
expect_error "missing spec file" "$w/base.json" "$w/nope.json" v0.1.0
expect_error "spec oasdiff can't load" "$w/base.json" "$w/broken.json" v0.1.0
expect_error "wrong argument count" "$w/base.json"

if [ "$failures" -gt 0 ]; then
  echo "$failures failed"
  exit 1
fi
echo "all passed"
