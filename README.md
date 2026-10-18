# Sinatra Go SDK

`sinatra-go` is the official Go client for [sinatra.dev](https://www.sinatra.dev), the hosted AI coding agent that turns Linear and GitHub issues into pull requests. Assign Sinatra an issue, mention it in Slack, or start a task from code, and it writes the change in its own sandbox and opens a draft pull request for your team to review.

With this client you can start, follow and stop Sinatra tasks and delegate Linear and GitHub issues from your own programs, CI jobs and tools, through the [Sinatra API](https://docs.sinatra.dev/docs/api-reference).

It needs Go 1.24 or later. The types are generated from the API's [OpenAPI spec](https://api.sinatra.dev/openapi.json), and a new version is released automatically when the spec changes. There are also official SDKs for [Node.js / TypeScript](https://github.com/sinatra-dev/sinatra-node) and [Python](https://github.com/sinatra-dev/sinatra-python); see [SDKs](https://docs.sinatra.dev/docs/sdks).

## Install

```sh
go get github.com/sinatra-dev/sinatra-go
```

```go
import "github.com/sinatra-dev/sinatra-go"
```

The package name is `sinatra`.

## Quick start

This is the body of `main` in [`examples/quickstart/main.go`](examples/quickstart/main.go), which has the imports and a `check` that calls `log.Fatal` on an error. It starts a task in your first workspace and prints the conversation until the turn ends:

```go
ctx := context.Background()
client, err := sinatra.NewClient() // reads SINATRA_TOKEN
check(err)
me, err := client.Me.Get(ctx)
check(err)
if len(me.Workspaces) == 0 {
	log.Fatal("the token's user isn't in any workspace yet")
}
ws := me.Workspaces[0].Id
created, err := client.Tasks.Create(ctx, ws, sinatra.CreateTaskRequest{Prompt: "Fix the flaky login test"},
	sinatra.WithIdempotencyKey("fix-flaky-login-1"))
check(err)
working := map[string]bool{"pending": true, "investigating": true, "implementing": true, "rework": true}
var after *string
for ; ; time.Sleep(3 * time.Second) {
	task, err := client.Tasks.Get(ctx, ws, created.Task.Id)
	check(err)
	for m, err := range client.Messages.ListAll(ctx, ws, task.Id, &sinatra.MessagesListParams{After: after}) {
		check(err)
		fmt.Printf("%s: %s\n", m.Role, m.Body)
		after = &m.Id
	}
	if !working[task.Status] {
		fmt.Println("status:", task.Status)
		return
	}
}
```

The API has no streaming. To wait for a reply, read the conversation after the last entry you've seen, and check the task's `Status`: the agent posts one reply per turn, and the turn is done when the task leaves the `working` statuses (for example, it reaches `replied` or `pr_open`). The loop polls every 3 seconds. The loop reads the status before the conversation, so it never stops before printing the reply that ended the turn. An unknown status counts as done, so the loop can't wait forever on a status added after this was written. See [How replies work](https://docs.sinatra.dev/docs/cli-and-api#how-replies-work).

## Examples

[`examples/`](examples) has small programs you can run as they are:

- [`quickstart`](examples/quickstart/main.go): the quick start above.
- [`delegate-issue`](examples/delegate-issue/main.go): hand Sinatra a Linear issue (`ENG-123`) or a GitHub issue (`acme/web#42`), for example from a CI job.
- [`search-issues`](examples/search-issues/main.go): list the open issues you could delegate, yours first.
- [`open-prs`](examples/open-prs/main.go): print the pull requests Sinatra has open in each workspace as a Markdown list, for a standup or a Slack post.

From a checkout:

```sh
SINATRA_TOKEN=sntr_... go run ./examples/open-prs
```

## Authentication

Create a personal access token on the [Access tokens](https://app.sinatra.dev/tokens) page (**CLI & API** in the dashboard). It starts with `sntr_` and acts as you in every workspace you belong to. `NewClient` reads it from the `SINATRA_TOKEN` environment variable, or you can pass it:

```go
client, err := sinatra.NewClient(sinatra.WithToken(os.Getenv("MY_TOKEN")))
```

`NewClient` returns an error if it finds no token. Other options:

- `WithBaseURL(url)`: defaults to `https://api.sinatra.dev`.
- `WithHTTPClient(*http.Client)`: defaults to `http.DefaultClient`.
- `WithTimeout(d)`: how long each attempt of a request may take, from sending it to reading the whole response. Defaults to 60 seconds; `0` turns it off. A timed-out attempt isn't retried, and its error wraps `context.DeadlineExceeded`.

Every method takes a `context.Context` first. Cancelling it stops the call, including a retry it's waiting to make, and a deadline on it limits the whole call, retries included:

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
task, err := client.Tasks.Get(ctx, workspaceID, taskID)
```

A `Client` is safe for concurrent use.

## Operations

The services mirror the API's operation ids:

| Method | Request |
| --- | --- |
| `Me.Get(ctx)` | `GET /v1/me` |
| `Me.RevokeToken(ctx)` | `DELETE /v1/me/token` |
| `Tasks.List(ctx, workspaceID, *TasksListParams)` | `GET /v1/workspaces/{workspaceId}/tasks` |
| `Tasks.Get(ctx, workspaceID, taskID)` | `GET /v1/workspaces/{workspaceId}/tasks/{taskId}` |
| `Tasks.Create(ctx, workspaceID, CreateTaskRequest, ...CallOption)` | `POST /v1/workspaces/{workspaceId}/tasks` |
| `Tasks.Stop(ctx, workspaceID, taskID)` | `POST /v1/workspaces/{workspaceId}/tasks/{taskId}/stop` |
| `Messages.Create(ctx, workspaceID, taskID, CreateMessageRequest, ...CallOption)` | `POST /v1/workspaces/{workspaceId}/tasks/{taskId}/messages` |
| `Messages.List(ctx, workspaceID, taskID, *MessagesListParams)` | `GET /v1/workspaces/{workspaceId}/tasks/{taskId}/messages` |
| `Tickets.Search(ctx, workspaceID, *TicketsSearchParams)` | `GET /v1/workspaces/{workspaceId}/tickets` |
| `Tickets.Delegate(ctx, workspaceID, DelegateTarget)` | `POST /v1/workspaces/{workspaceId}/tickets/delegate` |

Params arguments may be `nil`. Optional fields are pointers; `sinatra.Ptr` makes one: `&sinatra.TasksListParams{Status: sinatra.Ptr("pr_open")}`.

To delegate an issue, pass a `DelegateLinearTicket` or a `DelegateGithubTicket`. `Source` is filled in for you:

```go
client.Tickets.Delegate(ctx, workspaceID, sinatra.DelegateLinearTicket{Id: "ENG-123"})
client.Tickets.Delegate(ctx, workspaceID, sinatra.DelegateGithubTicket{Repo: "acme/web", Number: 42})
```

`Tasks.ListAll` and `Messages.ListAll` page through everything for you, as an `iter.Seq2`. They stop after the first error, which they yield:

```go
for task, err := range client.Tasks.ListAll(ctx, workspaceID, &sinatra.TasksListParams{Status: sinatra.Ptr("pr_open")}) {
	if err != nil {
		return err
	}
	fmt.Println(task.Id, task.Status)
}
```

Fields such as `Task.Status`, `Task.Source`, `Message.Role`, `Message.Kind` and the error `Code` are plain strings, and the API adds new values over time. Handle values you don't recognise.

## Retries and idempotency

`Tasks.Create` and `Messages.Create` always send an `Idempotency-Key`. If you don't pass one with `sinatra.WithIdempotencyKey`, the client makes a random UUID. Every retry of a call sends the same key, so a retried start never starts a second task. Pass your own key to make a retry safe across process restarts too.

The client retries a request up to 3 times when it's safe to repeat:

- `429`: after the `Retry-After` the server sends, at most 60 seconds.
- `503`: after 0.5s, then 1s, then 2s.

GETs, `Me.RevokeToken`, `Tasks.Create` and `Messages.Create` retry on both. `Tasks.Stop` retries on `503` only. `Tickets.Delegate` takes no idempotency key and is never retried. Other errors, timeouts and network failures are never retried.

## Errors

An error response is returned as a `*sinatra.Error`:

```go
_, err := client.Tasks.Stop(ctx, workspaceID, taskID)
var apiErr *sinatra.Error
if errors.As(err, &apiErr) && apiErr.Code == "not_running" {
	// the task had already stopped
} else if err != nil {
	return err
}
```

| Field | |
| --- | --- |
| `Status` | The HTTP status. |
| `Code` | The API's `error` code, such as `capacity_full`. `http_error` when the response carried none, such as an HTML error page from a proxy. |
| `Reason` | A sentence for a person. Don't branch on it. |
| `RequestID` | The request's id. Include it when you contact support. |
| `Body` | The raw response body. |
| `Header` | The response headers. |
| `Details` | The decoded body when it was JSON, with extra fields some refusals carry, such as `BillingUrl`. |

Network failures, timeouts and cancellations are not a `*sinatra.Error`: they are returned as `net/http` and `context` report them, so `errors.Is(err, context.DeadlineExceeded)` works.

The [API reference](https://docs.sinatra.dev/docs/api-reference) lists every operation's error codes.

## Versions

Releases follow semver. While the version is `0.x`, a breaking API change bumps the minor version and anything else bumps the patch. From `1.0`, additions bump the minor version, and a breaking change bumps the major version, which in Go also means a new module path ending in `/vN` (`github.com/sinatra-dev/sinatra-go/v2`) that you import instead. See [Major versions](RELEASING.md#major-versions). Pin a version in your `go.mod` as usual.

## Development

```sh
go generate ./...                  # regenerates api.gen.go from openapi.json
go vet ./... && go test -race ./...
bash scripts/contract.sh           # every operation against a Prism mock of openapi.json (needs Node.js 24)
bash scripts/install-oasdiff.sh    # the pinned oasdiff, into .tools/bin
PATH="$PWD/.tools/bin:$PATH" bash scripts/next-version.test.sh
bash scripts/check-readme.sh       # the README's quick start is examples/quickstart's, and builds against this module
```

`api.gen.go` is written by [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen), pinned in `generate.go`, with the settings in `codegen/`, and is never edited by hand. CI fails if it differs from what `openapi.json` generates. `openapi.json` is updated by the release workflow. See [RELEASING.md](RELEASING.md).

## License

MIT
