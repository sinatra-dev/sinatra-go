# Releasing

A Go module has no registry to publish to: a version exists once its `vX.Y.Z` tag is pushed. Anyone can then `go get` it, the Go module proxy caches it, and the checksum database (`sum.golang.org`) records its contents for good. So the tag is the release. It must point at exactly the code that was tested, and it is never moved or deleted.

Releases are automatic. `.github/workflows/release.yml` runs every hour, on every push to `main`, and on demand. It has two jobs, each with only the access it needs:

1. **build** (read-only token, no stored git credentials). Fetches the live spec from `https://api.sinatra.dev/openapi.json`. If it matches the committed `openapi.json` and no release-worthy commit landed since the last `vX.Y.Z` tag, the run stops there. Release-worthy means touching Go source other than tests, `go.mod`, `go.sum` or `codegen/`; docs, CI, scripts and test-only commits wait for the next release. Otherwise it writes the live spec to `openapi.json`, regenerates `api.gen.go`, builds, vets, and runs the unit tests (with `-race`), the contract tests against `prism mock openapi.json`, and the `next-version.sh` tests. It then computes the version with `scripts/next-version.sh` (`oasdiff` decides whether the spec change is breaking, additive or neither; with no tag yet it's `0.1.0`) and makes the release commit, `chore(release): vX.Y.Z`, locally. That commit holds `openapi.json` and `api.gen.go` exactly as tested, and the build fails if it changed anything else. With no spec change, the commit is empty. The commit (as a git bundle) and the release notes are uploaded as the run's `release` artifact.
2. **tag** (write token only, installs nothing). Checks the bundled commit: exactly one commit on the commit this job checked out, changing only `openapi.json` and `api.gen.go`, both regular files. It refuses if `main` moved since the build or if the tag already exists. Then it pushes the commit to `main` and the `vX.Y.Z` tag in one atomic push, creates the GitHub release, whose notes are the `oasdiff` changelog, and fetches `https://proxy.golang.org/github.com/sinatra-dev/sinatra-go/@v/vX.Y.Z.info`, so the proxy (and pkg.go.dev) has the version right away. The job fails if the proxy doesn't answer.

**tag** runs only when the repository variable `PUBLISH_ENABLED` is `true`, the run is on `main`, and it isn't a dry run. Otherwise **build** ends with a notice saying what a real run would release, and the run succeeds. Runs never overlap (concurrency group `release`).

## Setup

There is no registry account to create and no token to store: the workflow's own `GITHUB_TOKEN` pushes the tag.

1. Check that GitHub Actions may push to `main`: no branch rule or ruleset may block `github-actions[bot]` from pushing commits and `v*` tags. Under **Settings → Actions → General → Workflow permissions**, the default can stay read-only: `release.yml` asks for `contents: write` in the tag job only.

2. Set the repository variable that opens the gate:

   ```sh
   gh variable set PUBLISH_ENABLED --body true --repo sinatra-dev/sinatra-go
   ```

   (or **Settings → Secrets and variables → Actions → Variables → New repository variable**).

3. Start a release instead of waiting for the hourly run, watch it, then check the proxy:

   ```sh
   gh workflow run release.yml --repo sinatra-dev/sinatra-go
   sleep 10   # the run takes a moment to appear
   gh run watch --repo sinatra-dev/sinatra-go \
     "$(gh run list --workflow release.yml --event workflow_dispatch --repo sinatra-dev/sinatra-go --limit 1 --json databaseId --jq '.[0].databaseId')"
   go list -m github.com/sinatra-dev/sinatra-go@latest   # github.com/sinatra-dev/sinatra-go v0.1.0
   ```

   pkg.go.dev shows the new version within a few minutes: https://pkg.go.dev/github.com/sinatra-dev/sinatra-go

To stop releases at any time, set `PUBLISH_ENABLED` to anything but `true`.

## Tags are never moved

Never move, delete or re-push a `vX.Y.Z` tag, even one that points at a mistake. The proxy and the checksum database keep the first contents they saw, so a moved tag doesn't change what anyone downloads; it only makes `go` report a checksum mismatch (`SECURITY ERROR`) to anyone who fetches the version directly. The tag job refuses to push a tag that already exists.

To withdraw a bad version, release a fixed one and retract the bad one: add a `retract` directive to `go.mod` in a normal commit, for example

```
retract v0.3.1 // Decodes tasks.list wrongly. Use v0.3.2.
```

A `go.mod` change is release-worthy, so the next run releases a new version that carries the retraction, and `go get` stops choosing the retracted one.

## Major versions

Go treats `v2` and later as a different module, with its own path. `v0.x` and `v1.x` live at `github.com/sinatra-dev/sinatra-go`. When `next-version.sh` would go from `1.x` to `2.0.0` (a breaking API change after `1.0`), the module path has to become `github.com/sinatra-dev/sinatra-go/v2` first: change `module` in `go.mod` and every import of the package, in one change. Nothing does that yet. Until it's done, the build job's version step fails with `the next version is v2.0.0, but the module path is ...`, before anything is tagged, because a `v2.0.0` tag on a path without `/v2` is a version the `go` command refuses. While the version is `0.x`, breaking changes only bump the minor version, so this doesn't come up.

## When something fails

- **build fails** (the spec can't be fetched, the regenerated code doesn't compile, a test fails): nothing is tagged. Every later run fails the same way until the cause is fixed, for example by updating the wrapper for an operation the API removed.
- **build refuses the release commit** (`the build changed files a release commit can't hold`): a build step changed something besides `openapi.json` and `api.gen.go`, such as `go.mod` after the generated code started importing a new package. Make that change in a normal commit; the next run releases it.
- **tag refuses because `main` moved**: nothing is pushed. The push to `main` already started the run that releases the new `main`.
- **tag refuses the bundle** (`must have exactly one parent`, `changes <file>`, `not a regular file`, `the artifact was built from ...`): nothing is pushed. The artifact doesn't match what the build job makes, so treat it as tampering or a bug in `scripts/release.sh`, and look before re-running.
- **tag refuses because the tag exists**: someone pushed that version by hand, or a run you can't see did. Nothing is pushed. Leave the tag as it is (see above); the next run picks the version after it.
- **the push fails** (for example a branch rule rejects it): it's atomic, so neither the commit nor the tag is pushed. Fix the rule; the next run tries again.
- **the GitHub release fails after the push**: the tag is out. The next run notices the tag without a release, creates it and warms the proxy.
- **the proxy doesn't answer**: the tag and the GitHub release are out, and the version works: the proxy fetches it on the first `go get`. To get it listed now, run the `curl` the error prints:

  ```sh
  curl -sf https://proxy.golang.org/github.com/sinatra-dev/sinatra-go/@v/vX.Y.Z.info
  ```

## Dry runs

On GitHub, run the workflow with `dry_run` set: **build** runs in full, and **tag** is skipped.

```sh
gh workflow run release.yml --repo sinatra-dev/sinatra-go -f dry_run=true
```

Locally, **build**'s steps run with `scripts/release.sh`. They rewrite `openapi.json` and make a local release commit, so use a scratch clone:

```sh
git clone https://github.com/sinatra-dev/sinatra-go.git /tmp/sinatra-go-dry-run && cd /tmp/sinatra-go-dry-run
bash scripts/install-oasdiff.sh
bash scripts/release.sh plan        # SPEC_FILE=path/to/spec.json to use a local spec; needs gh auth
go generate ./... && go build ./... && go vet ./... && go test -race ./...
bash scripts/contract.sh && PATH="$PWD/.tools/bin:$PATH" bash scripts/next-version.test.sh
bash scripts/release.sh version     # prints the version and the release notes
bash scripts/release.sh pack        # the release commit and bundle, in release-state/
DRY_RUN=true bash scripts/release.sh gate
```

Don't run `tag` locally: it pushes to GitHub.

## Tools

- `oapi-codegen` is pinned in `generate.go`'s `go:generate` line (`go run ...@v2.8.0`), so it never enters `go.mod`. `scripts/derive-templates.sh` writes the template overrides in `codegen/` from that same version. To upgrade, change the version in `generate.go`, run `go generate ./...`, and commit the new `api.gen.go` and `codegen/` together, or CI fails the drift check.
- Prism is pinned in `scripts/contract.sh` (`@stoplight/prism-cli@5.16.0`, through `npx`). It needs Node.js 24.
- `oasdiff` is pinned, with checksums, in `scripts/install-oasdiff.sh`.
- staticcheck and govulncheck are pinned in `.github/workflows/ci.yml`.

`scripts/next-version.sh` and `scripts/next-version.test.sh` are shared byte for byte with `sinatra-node` and `sinatra-python`. Change them in all three.

GitHub turns off scheduled workflows in a public repository after 60 days without activity. If releases stop, re-enable the workflow on the **Actions** tab.
