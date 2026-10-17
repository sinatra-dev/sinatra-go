#!/usr/bin/env bash
# Fails if regenerating from the committed openapi.json changes api.gen.go or
# the derived templates in codegen/: the generated code must always be exactly
# what the pinned generator makes from the committed spec. Fix with
# `go generate ./...` and commit the result.
set -euo pipefail
cd "$(dirname "$0")/.."

paths=(api.gen.go codegen)

go generate ./...

if ! git diff --quiet --exit-code -- "${paths[@]}" ||
  [ -n "$(git ls-files --others --exclude-standard -- "${paths[@]}")" ]; then
  echo "api.gen.go is out of date with openapi.json and codegen/. Run 'go generate ./...' and commit the result:" >&2
  git status --short -- "${paths[@]}" >&2
  git --no-pager diff --stat -- "${paths[@]}" >&2
  exit 1
fi
echo "api.gen.go matches openapi.json"
