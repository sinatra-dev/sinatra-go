#!/usr/bin/env bash
# Prints the SDK version to release next, judged from how the API spec changed.
#
#   scripts/next-version.sh <old-spec.json> <new-spec.json> [<last-tag>]
#
#   <old-spec.json>  the spec the last release was built from
#   <new-spec.json>  the spec to release now
#   <last-tag>       the last release tag, such as v0.3.1; empty or absent
#                    when nothing has been released yet
#
# The change is classified with oasdiff:
#   breaking  `oasdiff breaking --fail-on ERR` exits 1
#   additive  otherwise, `oasdiff changelog` lists at least one change
#   patch     otherwise (identical specs, prose-only edits, SDK-only fixes)
#
# and mapped to a version:
#   no last tag   0.1.0
#   0.x           breaking -> minor, anything else -> patch
#   1.0 and up    breaking -> major, additive -> minor, else patch
#
# Prints only the version (no "v") on stdout. Exits non-zero, printing
# nothing on stdout, if oasdiff can't compare the specs or the tag isn't
# vX.Y.Z: a failed comparison never counts as "no change".
#
# Needs: bash, oasdiff, jq. Set OASDIFF to use an oasdiff that isn't on PATH.
# This file is shared byte for byte by sinatra-node, sinatra-python and
# sinatra-go; change it in all three.
set -euo pipefail

die() {
  echo "next-version: $*" >&2
  exit 2
}

[ "$#" -eq 2 ] || [ "$#" -eq 3 ] || die "usage: next-version.sh <old-spec.json> <new-spec.json> [<last-tag>]"
old_spec=$1
new_spec=$2
last_tag=${3:-}

if [ -z "$last_tag" ]; then
  echo "0.1.0"
  exit 0
fi

if [[ ! "$last_tag" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  die "last tag '$last_tag' is not vX.Y.Z"
fi
major=$((10#${BASH_REMATCH[1]}))
minor=$((10#${BASH_REMATCH[2]}))
patch=$((10#${BASH_REMATCH[3]}))

# oasdiff reads "<ref>:<path>" as a git revision; only accept plain files.
for spec in "$old_spec" "$new_spec"; do
  [ -f "$spec" ] || die "spec '$spec' is not a file"
done

oasdiff=${OASDIFF:-oasdiff}
command -v "$oasdiff" >/dev/null 2>&1 || die "oasdiff not found (set OASDIFF or put it on PATH)"
command -v jq >/dev/null 2>&1 || die "jq not found"

# oasdiff breaking exits 0 for no breaking change, 1 for a breaking change,
# and something else (such as 102 for a spec it can't load) on failure.
set +e
"$oasdiff" breaking "$old_spec" "$new_spec" --fail-on ERR --format json >/dev/null
breaking_status=$?
set -e
case "$breaking_status" in
  0) change=none ;;
  1) change=breaking ;;
  *) die "oasdiff breaking failed with exit $breaking_status" ;;
esac

if [ "$change" = none ]; then
  # The text format is never empty ("No changes detected"), so count the
  # entries of the JSON format instead.
  changelog=$("$oasdiff" changelog "$old_spec" "$new_spec" --format json) ||
    die "oasdiff changelog failed"
  count=$(printf '%s' "$changelog" | jq 'if type == "array" then length else error("not an array") end') ||
    die "oasdiff changelog printed something that isn't a JSON array"
  if [ "$count" -gt 0 ]; then
    change=additive
  fi
fi

if [ "$major" -eq 0 ]; then
  case "$change" in
    breaking) echo "0.$((minor + 1)).0" ;;
    *) echo "0.${minor}.$((patch + 1))" ;;
  esac
else
  case "$change" in
    breaking) echo "$((major + 1)).0.0" ;;
    additive) echo "${major}.$((minor + 1)).0" ;;
    *) echo "${major}.${minor}.$((patch + 1))" ;;
  esac
fi
