//go:build contract

// Contract tests: every operation, sent through the client, against a Prism
// mock of the committed openapi.json. Prism runs with --errors, so a request
// that doesn't match the spec (path, query, headers, body, auth) is refused
// and the call fails. Run them with scripts/contract.sh, which starts Prism
// and sets SINATRA_CONTRACT_URL.

package sinatra

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
)

const (
	contractWorkspace = "ws_contract"
	contractTask      = "task_contract"
)

func contractClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	baseURL := os.Getenv("SINATRA_CONTRACT_URL")
	if baseURL == "" {
		t.Fatal("SINATRA_CONTRACT_URL is not set: run scripts/contract.sh")
	}
	c, err := NewClient(append([]Option{WithToken("sntr_contract"), WithBaseURL(baseURL)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestContractEveryOperation(t *testing.T) {
	c := contractClient(t)
	ctx := context.Background()
	ws, task := contractWorkspace, contractTask

	t.Run("me.get", func(t *testing.T) {
		me, err := c.Me.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if me.User.Id == "" || me.Workspaces == nil {
			t.Errorf("me = %+v", me)
		}
	})
	t.Run("me.revokeToken", func(t *testing.T) {
		if err := c.Me.RevokeToken(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tasks.list", func(t *testing.T) {
		page, err := c.Tasks.List(ctx, ws, &TasksListParams{Status: Ptr("pr_open"), Limit: Ptr(100), Cursor: Ptr("opaque")})
		if err != nil {
			t.Fatal(err)
		}
		if page.Tasks == nil {
			t.Errorf("page = %+v", page)
		}
	})
	t.Run("tasks.create", func(t *testing.T) {
		created, err := c.Tasks.Create(ctx, ws, CreateTaskRequest{Prompt: "Fix the flaky test", Title: Ptr("Flaky"), Repo: Ptr("acme/web")})
		if err != nil {
			t.Fatal(err)
		}
		if created.Task.Id == "" || created.MessageId == "" {
			t.Errorf("created = %+v", created)
		}
	})
	t.Run("tasks.create with your own key", func(t *testing.T) {
		if _, err := c.Tasks.Create(ctx, ws, CreateTaskRequest{Prompt: "Fix it"}, WithIdempotencyKey("ci-run-42:fix-it")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tasks.get", func(t *testing.T) {
		got, err := c.Tasks.Get(ctx, ws, task)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "" {
			t.Errorf("task = %+v", got)
		}
	})
	t.Run("tasks.stop", func(t *testing.T) {
		stopped, err := c.Tasks.Stop(ctx, ws, task)
		if err != nil {
			t.Fatal(err)
		}
		if !stopped.Accepted {
			t.Errorf("stop = %+v", stopped)
		}
	})
	t.Run("messages.create", func(t *testing.T) {
		sent, err := c.Messages.Create(ctx, ws, task, CreateMessageRequest{Text: "Also update the docs"})
		if err != nil {
			t.Fatal(err)
		}
		if sent.MessageId == "" {
			t.Errorf("sent = %+v", sent)
		}
	})
	t.Run("messages.list", func(t *testing.T) {
		page, err := c.Messages.List(ctx, ws, task, &MessagesListParams{After: Ptr("entry_1"), Limit: Ptr(200)})
		if err != nil {
			t.Fatal(err)
		}
		if page.Messages == nil {
			t.Errorf("page = %+v", page)
		}
	})
	t.Run("tickets.search", func(t *testing.T) {
		found, err := c.Tickets.Search(ctx, ws, &TicketsSearchParams{Q: Ptr("flaky"), Repo: Ptr("acme/web")})
		if err != nil {
			t.Fatal(err)
		}
		if found.Tickets == nil {
			t.Errorf("found = %+v", found)
		}
	})
	t.Run("tickets.delegate (linear)", func(t *testing.T) {
		ok, err := c.Tickets.Delegate(ctx, ws, DelegateLinearTicket{Id: "ENG-123"})
		if err != nil {
			t.Fatal(err)
		}
		if !ok.Ok {
			t.Errorf("delegate = %+v", ok)
		}
	})
	t.Run("tickets.delegate (github)", func(t *testing.T) {
		ok, err := c.Tickets.Delegate(ctx, ws, DelegateGithubTicket{Repo: "acme/web", Number: 7})
		if err != nil {
			t.Fatal(err)
		}
		if !ok.Ok {
			t.Errorf("delegate = %+v", ok)
		}
	})
}

// Guards the suite itself: if Prism stopped validating requests, every test
// above would pass vacuously.
func TestContractTheMockRefusesWhatTheSpecForbids(t *testing.T) {
	ctx := context.Background()

	t.Run("a prompt that breaks the schema is refused", func(t *testing.T) {
		_, err := contractClient(t).Tasks.Create(ctx, contractWorkspace, CreateTaskRequest{Prompt: ""})
		var e *Error
		if !errors.As(err, &e) || e.Status < 400 {
			t.Fatalf("err = %v, want an *Error with a 4xx status", err)
		}
	})
	t.Run("a request without a token is refused", func(t *testing.T) {
		anonymous := contractClient(t, WithHTTPClient(&http.Client{Transport: dropAuthorization{}}))
		_, err := anonymous.Me.Get(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Status != http.StatusUnauthorized {
			t.Fatalf("err = %v, want a 401 *Error", err)
		}
	})
}

type dropAuthorization struct{}

func (dropAuthorization) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Del("Authorization")
	return http.DefaultTransport.RoundTrip(r)
}
