# sinatra-go

A Go client for the [Sinatra API](https://docs.sinatra.dev/docs/api-reference): start, follow and stop Sinatra tasks, and delegate Linear and GitHub issues.

It needs Go 1.24 or later. The types are generated from the API's [OpenAPI spec](https://api.sinatra.dev/openapi.json), and a new version is released automatically when the spec changes.

## Install

```sh
go get github.com/sinatra-dev/sinatra-go
```

```go
import "github.com/sinatra-dev/sinatra-go"
```

The package name is `sinatra`.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/sinatra-dev/sinatra-go"
)

func main() {
	ctx := context.Background()
	client, err := sinatra.NewClient() // reads SINATRA_TOKEN
	if err != nil {
		log.Fatal(err)
	}
	me, err := client.Me.Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if len(me.Workspaces) == 0 {
		log.Fatal("the token's user isn't in any workspace yet")
	}
	ws := me.Workspaces[0].Id

	created, err := client.Tasks.Create(ctx, ws,
		sinatra.CreateTaskRequest{Prompt: "Fix the flaky login test", Repo: sinatra.Ptr("acme/web")},
		sinatra.WithIdempotencyKey("fix-flaky-login-1"))
	if err != nil {
		log.Fatal(err)
	}

	// The turn is done when the task leaves these statuses (for example, it reaches replied or pr_open).
	working := map[string]bool{"pending": true, "investigating": true, "implementing": true, "rework": true}
	var after *string
	for {
		task, err := client.Tasks.Get(ctx, ws, created.Task.Id)
		if err != nil {
			log.Fatal(err)
		}
		for m, err := range client.Messages.ListAll(ctx, ws, task.Id, &sinatra.MessagesListParams{After: after}) {
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("%s: %s\n", m.Role, m.Body)
			after = sinatra.Ptr(m.Id)
		}
		if !working[task.Status] {
			fmt.Println("status:", task.Status)
			return
		}
		time.Sleep(3 * time.Second)
	}
}
```

The API has no streaming. To wait for a reply, read the conversation after the last entry you've seen, and check the task's `Status`: the agent posts one reply per turn, and the turn is done when the task stops working. The loop reads the status before the conversation, so it never stops before printing the reply that ended the turn. An unknown status counts as done, so the loop can't wait forever on a status added after this was written. See [How replies work](https://docs.sinatra.dev/docs/cli-and-api#how-replies-work).

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

Releases follow semver. While the version is `0.x`, a breaking API change bumps the minor version and anything else bumps the patch. From `1.0`, breaking changes bump the major version and additions bump the minor. Pin a version in your `go.mod` as usual.

## Development

```sh
go generate ./...                  # regenerates api.gen.go from openapi.json
go vet ./... && go test -race ./...
bash scripts/contract.sh           # every operation against a Prism mock of openapi.json (needs Node.js 24)
bash scripts/install-oasdiff.sh    # the pinned oasdiff, into .tools/bin
PATH="$PWD/.tools/bin:$PATH" bash scripts/next-version.test.sh
bash scripts/check-readme.sh       # builds this README's quick start against the local module
```

`api.gen.go` is written by [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen), pinned in `generate.go`, with the settings in `codegen/`, and is never edited by hand. CI fails if it differs from what `openapi.json` generates. `openapi.json` is updated by the release workflow. See [RELEASING.md](RELEASING.md).

## License

MIT
