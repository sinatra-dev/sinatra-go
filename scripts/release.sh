#!/usr/bin/env bash
# The release steps that .github/workflows/release.yml runs, one per command.
# Each can be run locally for a dry run (see RELEASING.md).
#
# For a Go module the tag is the release: once vX.Y.Z is pushed, anyone can
# `go get` it, and the checksum database records its contents for good. So
# the tag must point at exactly the tree the build tested, and is never moved
# or deleted.
#
# Job "build" (read-only token, runs the toolchain and the tests):
#   plan     Fetch the live spec and decide whether to release: stop if it
#            matches openapi.json and no release-worthy commit landed since the
#            last tag; otherwise write it to openapi.json. Also notices a tag
#            whose GitHub release is missing (a run that failed after its push).
#   version  Compute the next version from the spec change and write the
#            release notes.
#   pack     Commit the release locally (openapi.json and api.gen.go, as
#            tested; refuses if anything else in the tree changed) and save
#            that commit as a git bundle. Everything the tag job needs is then
#            in release-state/, which the workflow uploads as an artifact.
#   gate     Say whether the tag job will run, and why not.
# Job "verify" (read-only token, Go and the pinned oasdiff only: no npm, no
# Prism, no cache):
#   verify   Check the bundled release commit independently of everything
#            build ran: its shape, its version (recomputed with
#            next-version.sh), that its openapi.json is the
#            live spec and steers no generator option, that the pinned
#            generator makes exactly its api.gen.go from it, and that it
#            builds, vets and passes the unit tests.
# Job "tag" (contents: write, installs nothing):
#   tag      Create the missing GitHub release, if any. Check the bundled
#            release commit is the one verify passed, refuse if main moved,
#            push the commit and its vX.Y.Z tag to main in one atomic push,
#            create the GitHub release, then fetch the version from the Go
#            module proxy so it's listed.
#
# Environment:
#   SPEC_URL         the live spec (default https://api.sinatra.dev/openapi.json)
#   SPEC_FILE        read the spec from this file instead of SPEC_URL
#   DRY_RUN          "true" for a workflow_dispatch dry run (gate's notice only)
#   PUBLISH_ENABLED  "true" when releasing is on (gate's notice only; the
#                    workflow's job condition is the real gate)
#   RELEASE_BRANCH   the branch releases come from and go to (default main)
#   GOPROXY_URL      the module proxy to warm (default https://proxy.golang.org)
#   VERIFIED_SHA, VERIFIED_VERSION
#                    tag: the release commit and version the verify job passed
set -euo pipefail
cd "$(dirname "$0")/.."

# The module path, from go.mod (sed, not go: the tag job installs nothing).
MODULE=$(sed -n 's/^module[[:space:]]*//p' go.mod)
[ -n "$MODULE" ] || { echo "release: no module path in go.mod" >&2; exit 1; }
SPEC_URL=${SPEC_URL:-https://api.sinatra.dev/openapi.json}
BRANCH=${RELEASE_BRANCH:-main}
GOPROXY_URL=${GOPROXY_URL:-https://proxy.golang.org}
STATE=release-state
# Only commits touching these count as a reason to release: the Go source
# (not tests), the module files and the generator settings. Docs, CI, scripts
# and test-only commits wait for the next release.
RELEASE_PATHS=('*.go' ':(exclude)*_test.go' ':(exclude)examples' go.mod go.sum codegen)
# The only files a release commit may change.
RELEASE_FILES=(openapi.json api.gen.go)
PATH="$PWD/.tools/bin:$PATH"

die() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::error title=release::$*"; else echo "release: $*" >&2; fi
  exit 1
}
notice() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::notice title=release::$*"; else echo "notice: $*"; fi
}
output() {
  if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "$1" >>"$GITHUB_OUTPUT"; fi
}
summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat >>"$GITHUB_STEP_SUMMARY"; else cat; fi
}
canonical() { jq -S -c . "$1"; }
state() { cat "$STATE/$1"; }
git_bot() {
  git -c user.name="github-actions[bot]" -c user.email="41898099+github-actions[bot]@users.noreply.github.com" "$@"
}

# Release tags, highest version first. By version, not by ancestry: a tag
# pushed by hand to recover a failed run need not be on main.
release_tags() { git tag --list 'v*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; }

# Commits in <range> that touch the release paths, without release commits.
release_commits() {
  git log --no-merges --invert-grep --grep='^chore(release): ' --format='- %s (%h)' "$1" -- "${RELEASE_PATHS[@]}"
}

# write_notes <from-tag or ""> <to-rev> <old-spec> <new-spec> <out>
write_notes() {
  local from=$1 to=$2 old=$3 new=$4 out=$5 levels="$STATE/severity-levels.txt"
  # The spec's info.version is not the SDK's version: leave that check out.
  printf 'api-version-not-bumped none\n' >"$levels"
  local changelog=(oasdiff changelog "$old" "$new" --severity-levels "$levels")
  {
    if [ -z "$from" ]; then
      echo "First release of \`$MODULE\`, generated from the Sinatra API spec."
    else
      if [ "$("${changelog[@]}" --format json | jq length)" -gt 0 ]; then
        # oasdiff's markdown has its own "## API Changes" section; drop its
        # title line ("# API Changelog 1.0.0 vs. 1.0.0").
        "${changelog[@]}" --format markdown | sed '1d' | cat -s
      else
        printf '## API Changes\n\nNone.\n'
      fi
      local log
      log=$(release_commits "$from..$to")
      if [ -n "$log" ]; then
        printf '\n## Other changes\n\n%s\n' "$log"
      fi
    fi
    # shellcheck disable=SC2016 # Markdown backticks, not a command
    printf '\nInstall: `go get %s@%s`\n' "$MODULE" "${VERSION_TAG:-$to}"
    printf '\nAPI reference: https://docs.sinatra.dev/docs/api-reference\n'
  } >"$out"
}

# fetch_spec <out>: the live spec (or SPEC_FILE), checked to be OpenAPI.
fetch_spec() {
  if [ -n "${SPEC_FILE:-}" ]; then
    cp "$SPEC_FILE" "$1"
  else
    curl -fsSL --retry 3 --retry-all-errors --max-time 60 -o "$1" "$SPEC_URL"
  fi
  jq -e '(.openapi | type == "string") and (.paths | type == "object")' "$1" >/dev/null ||
    die "the spec from ${SPEC_FILE:-$SPEC_URL} isn't an OpenAPI document"
}

# check_major <version>: Go needs v2 and up under their own module path
# (.../v2). Tagging v2.0.0 on this path would push a tag the go command
# rejects, so stop first.
check_major() {
  local version=$1 major=${1%%.*} want root=$MODULE
  [[ "$root" =~ /v[0-9]+$ ]] && root=${root%/v*}
  want=$root
  [ "$major" -lt 2 ] || want=$root/v$major
  [ "$MODULE" = "$want" ] ||
    die "the next version is v$version, but the module path is $MODULE: v$version needs $want. See RELEASING.md, 'Major versions'."
}

step_plan() {
  rm -rf "$STATE"
  mkdir -p "$STATE"
  git rev-parse HEAD >"$STATE/base-sha"

  fetch_spec "$STATE/live.json"

  local tags tag previous
  tags=$(release_tags)
  tag=$(sed -n 1p <<<"$tags")
  previous=$(sed -n 2p <<<"$tags")
  echo "$tag" >"$STATE/last-tag"

  # A tag without a GitHub release: a run pushed its tag, then failed.
  local missing=""
  if [ -n "$tag" ]; then
    local err
    if ! err=$(gh release view "$tag" --json tagName 2>&1 >/dev/null); then
      case "$err" in
        *"release not found"*) missing=$tag ;;
        *) die "couldn't check the GitHub release for $tag: $err" ;;
      esac
    fi
  fi
  if [ -n "$missing" ]; then
    echo "$missing" >"$STATE/missing-tag"
    git show "$missing:openapi.json" >"$STATE/missing-new.json"
    if [ -n "$previous" ]; then
      git show "$previous:openapi.json" >"$STATE/missing-old.json"
    else
      cp "$STATE/missing-new.json" "$STATE/missing-old.json"
    fi
    VERSION_TAG=$missing write_notes "$previous" "$missing" "$STATE/missing-old.json" "$STATE/missing-new.json" "$STATE/missing-notes.md"
    echo "$missing has no GitHub release; the tag job will create it."
  fi
  output "missing_release=$missing"

  if [ -n "$tag" ]; then
    git show "$tag:openapi.json" >"$STATE/old.json"
  else
    cp openapi.json "$STATE/old.json"
  fi

  local spec_changed=true commits=none
  if [ "$(canonical "$STATE/live.json")" = "$(canonical openapi.json)" ]; then spec_changed=false; fi
  if [ -n "$tag" ]; then commits=$(git rev-list --count "$tag..HEAD" -- "${RELEASE_PATHS[@]}"); fi

  if [ -n "$tag" ] && [ "$spec_changed" = false ] && [ "$commits" = 0 ]; then
    echo "Nothing to release: the live spec matches openapi.json and no release-worthy commit landed since $tag."
    output "release=false"
    return 0
  fi

  # Keep the live document's key order; pretty-print so diffs stay readable.
  jq . "$STATE/live.json" >openapi.json
  echo "Releasing: last tag ${tag:-none}, spec changed: $spec_changed, release-worthy commits since the tag: $commits."
  output "release=true"
}

step_version() {
  local tag version
  tag=$(state last-tag)
  version=$(bash scripts/next-version.sh "$STATE/old.json" openapi.json "$tag")
  check_major "$version"
  echo "$version" >"$STATE/version"
  VERSION_TAG="v$version" write_notes "$tag" HEAD "$STATE/old.json" openapi.json "$STATE/notes.md"

  output "version=$version"
  {
    echo "### v$version (last release: ${tag:-none})"
    echo
    cat "$STATE/notes.md"
  } | summary
}

step_pack() {
  local version base
  version=$(state version)
  base=$(state base-sha)
  [ "$(git rev-parse HEAD)" = "$base" ] || die "HEAD moved from $base during the build"

  # The release commit holds exactly the tree that was tested: the spec and
  # the code generated from it. Anything else the build changed (go.mod,
  # go.sum, a stray file) means the tested tree isn't the one we'd tag.
  git add -- "${RELEASE_FILES[@]}"
  local stray
  stray=$(git status --porcelain --untracked-files=all | grep -v -E '^M  (openapi\.json|api\.gen\.go)$' || true)
  [ -z "$stray" ] || die "the build changed files a release commit can't hold: $stray"
  # With no spec change the commit is empty; it still marks the release.
  git_bot commit -q --allow-empty -m "chore(release): v$version"
  [ -z "$(git status --porcelain --untracked-files=all)" ] || die "the tree isn't clean after the release commit"
  git rev-parse HEAD >"$STATE/release-sha"
  git bundle create "$STATE/release.bundle" "$base..HEAD" 2>/dev/null
  echo "Release commit $(state release-sha) for v$version, on $base."
}

step_gate() {
  local what jobs="The verify and tag jobs run"
  if [ -s "$STATE/version" ]; then
    what="release v$(state version)"
  else
    what="create the missing GitHub release for $(state missing-tag)"
    jobs="The tag job runs" # verify only runs when there's a release commit
  fi
  if [ "${DRY_RUN:-false}" = "true" ]; then
    notice "Dry run: verify still runs, the tag job is skipped. A real run would $what."
  elif [ "${PUBLISH_ENABLED:-}" != "true" ]; then
    notice "PUBLISH_ENABLED isn't 'true' (or this isn't $BRANCH): verify still runs, the tag job is skipped. A real run would $what. See RELEASING.md."
  else
    echo "$jobs next, to $what."
  fi
}

# The commit main points at on the remote right now.
remote_head() {
  local line
  line=$(git ls-remote origin "refs/heads/$BRANCH") || die "couldn't read $BRANCH from origin"
  cut -f1 <<<"$line"
}

# Checks the release commit from the artifact before it is pushed: one commit
# on top of the commit this job checked out (the one build started from),
# that changes only openapi.json and api.gen.go, both as regular files.
verify_release_commit() {
  local sha=$1 base parents path list
  base=$(git rev-parse HEAD)
  [ "$(state base-sha)" = "$base" ] ||
    die "the artifact was built from $(state base-sha), but this job checked out $base"
  parents=$(git rev-list --parents -n 1 "$sha" | cut -d' ' -f2-)
  [ "$parents" = "$base" ] ||
    die "release commit $sha must have exactly one parent, $base; it has: ${parents:-none}"
  # --no-renames: a rename would otherwise list only its destination, so a
  # file moved onto api.gen.go would pass and its source would be deleted.
  list=$(mktemp)
  git diff --no-renames --name-only -z "$base" "$sha" >"$list" || die "couldn't list the files release commit $sha changes"
  while IFS= read -r -d '' path; do
    case "$path" in
      openapi.json | api.gen.go) ;;
      *) die "release commit $sha changes $path; a release commit may only change ${RELEASE_FILES[*]}" ;;
    esac
  done <"$list"
  rm -f "$list"
  local mode
  for path in "${RELEASE_FILES[@]}"; do
    mode=$(git ls-tree "$sha" -- "$path" | cut -d' ' -f1)
    [ "$mode" = 100644 ] || die "release commit $sha has $path as mode ${mode:-missing}, not a regular file"
  done
}

# Asks the module proxy for the new version, so it's fetched (and listed on
# pkg.go.dev) now rather than on the first `go get`.
warm_proxy() {
  local tag=$1 info
  info=$(curl -sf --retry 5 --retry-delay 10 --retry-all-errors --max-time 60 "$GOPROXY_URL/$MODULE/@v/$tag.info") ||
    die "$GOPROXY_URL didn't answer for $MODULE@$tag. The tag is pushed; re-run: curl -sf $GOPROXY_URL/$MODULE/@v/$tag.info"
  [ "$(jq -r .Version <<<"$info")" = "$tag" ] || die "$GOPROXY_URL answered for $MODULE@$tag with: $info"
  echo "$GOPROXY_URL has $MODULE@$tag: $info"
}

# Generator options a spec could set through extensions. oapi-codegen
# v2.8.0 honours these; x-go-type-import, for one, adds any import to
# api.gen.go. The API's spec uses none, so any of them is refused.
GENERATOR_KEYS='^x-(go|oapi-codegen)-|^x-(client-response-wrapper|deprecated-reason|enum-varnames|enumnames|omitempty|omitzero|order)$'

# version_above <a> <b>: whether X.Y.Z a is higher than X.Y.Z b.
version_above() {
  local IFS=. a b i
  read -r -a a <<<"$1"
  read -r -a b <<<"$2"
  for i in 0 1 2; do
    ((10#${a[i]} > 10#${b[i]})) && return 0
    ((10#${a[i]} < 10#${b[i]})) && return 1
  done
  return 1
}

step_verify() {
  [ -s "$STATE/version" ] || die "the artifact has no version to verify"
  local version sha base state_dir highest
  version=$(state version)
  sha=$(state release-sha)
  base=$(git rev-parse HEAD)
  state_dir=$PWD/$STATE

  # The commit: one commit on this job's checkout, only the two files.
  git fetch -q "$STATE/release.bundle" HEAD
  [ "$(git rev-parse FETCH_HEAD)" = "$sha" ] || die "release.bundle doesn't hold release commit $sha"
  verify_release_commit "$sha"

  # The version: X.Y.Z, above every existing tag, and a module path that
  # allows its major version.
  [[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
    die "the artifact's version '$version' isn't X.Y.Z"
  highest=$(release_tags | sed -n 1p)
  if [ -n "$highest" ] && ! version_above "$version" "${highest#v}"; then
    die "the artifact's version $version isn't above the highest tag, $highest"
  fi
  check_major "$version"
  # And exactly the version next-version.sh gives, run here with the pinned
  # oasdiff: the highest tag's spec (what build's plan compares against) to
  # the release commit's.
  local expected
  if [ -n "$highest" ]; then
    git show "$highest:openapi.json" >"$STATE/verify-old.json"
  else
    git show "$sha:openapi.json" >"$STATE/verify-old.json"
  fi
  git show "$sha:openapi.json" >"$STATE/verify-new.json"
  expected=$(bash scripts/next-version.sh "$STATE/verify-old.json" "$STATE/verify-new.json" "$highest") ||
    die "next-version.sh failed on the release commit's spec"
  [ "$version" = "$expected" ] ||
    die "the artifact's version is $version, but the spec change since ${highest:-the start} makes it $expected"

  # The spec: the live one, byte for byte as build writes it (jq .), and
  # without generator options.
  fetch_spec "$STATE/verify-live.json"
  git show "$sha:openapi.json" >"$STATE/verify-bundle.json"
  jq . "$STATE/verify-live.json" | cmp -s - "$STATE/verify-bundle.json" ||
    die "release commit $sha's openapi.json isn't the live spec: the API changed since the build, or the bundle was altered. Not releasing; the next run builds from the live spec."
  local keys
  keys=$(jq -r --arg re "$GENERATOR_KEYS" '[.. | objects | keys[] | select(ascii_downcase | test($re))] | unique | join(", ")' "$STATE/verify-bundle.json")
  [ -z "$keys" ] || die "release commit $sha's openapi.json sets generator options ($keys); refusing to generate from it"

  # The code: regenerate api.gen.go in a worktree of the release commit with
  # the base commit's generator settings, and require the same bytes. The
  # release commit's api.gen.go is removed first, so `go generate` only reads
  # directives from files the release commit can't change.
  local wt
  wt=$(mktemp -d)
  git worktree add -q --detach "$wt" "$sha"
  # shellcheck disable=SC2064 # expand $wt now
  trap "git worktree remove --force '$wt' 2>/dev/null || true" EXIT
  (
    cd "$wt"
    git checkout -q "$base" -- generate.go codegen scripts/derive-templates.sh
    cp api.gen.go "$state_dir/verify-bundle-api.gen.go"
    rm api.gen.go
    go generate ./...
    cmp -s api.gen.go "$state_dir/verify-bundle-api.gen.go" ||
      die "release commit $sha's api.gen.go isn't what oapi-codegen generates from its openapi.json: $(diff "$state_dir/verify-bundle-api.gen.go" api.gen.go | head -20 | tr '\n' ' ')"
    [ -z "$(git status --porcelain --untracked-files=all)" ] || die "regenerating changed more than api.gen.go: $(git status --porcelain)"
    go build ./...
    go vet ./...
    go test -race -count=1 ./...
  )
  git worktree remove --force "$wt"

  output "release_sha=$sha"
  output "version=$version"
  echo "Verified release commit $sha for v$version."
}

step_tag() {
  if [ -s "$STATE/missing-tag" ]; then
    local missing
    missing=$(state missing-tag)
    gh release create "$missing" --verify-tag --title "$missing" --notes-file "$STATE/missing-notes.md"
    warm_proxy "$missing"
  fi
  [ -s "$STATE/version" ] || return 0

  local version sha base head
  version=$(state version)
  sha=$(state release-sha)
  git fetch -q "$STATE/release.bundle" HEAD
  [ "$(git rev-parse FETCH_HEAD)" = "$sha" ] || die "release.bundle doesn't hold release commit $sha"
  verify_release_commit "$sha"
  [ "${VERIFIED_SHA:-}" = "$sha" ] && [ "${VERIFIED_VERSION:-}" = "$version" ] ||
    die "the verify job passed '${VERIFIED_SHA:-nothing}' as '${VERIFIED_VERSION:-nothing}', not release commit $sha as $version; not releasing"

  base=$(state base-sha)
  head=$(remote_head)
  [ "$head" = "$base" ] ||
    die "$BRANCH moved from $base to $head since this run built; not releasing v$version. The next run releases the new $BRANCH."
  # Tags are never moved: a version that exists stays what it is.
  [ -z "$(git ls-remote origin "refs/tags/v$version")" ] || die "v$version already exists on origin; tags are never moved"

  git_bot tag -a "v$version" "$sha" -m "v$version"
  git push --atomic origin "$sha:refs/heads/$BRANCH" "refs/tags/v$version"
  gh release create "v$version" --verify-tag --title "v$version" --notes-file "$STATE/notes.md"
  warm_proxy "v$version"
}

case "${1:-}" in
  plan | version | pack | gate | verify | tag) "step_$1" ;;
  *) die "usage: release.sh plan|version|pack|gate|verify|tag" ;;
esac
