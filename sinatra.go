// Package sinatra is the official Go client for sinatra.dev, the AI coding
// agent that turns Linear and GitHub issues into pull requests: start, follow
// and stop Sinatra tasks, and delegate Linear and GitHub issues.
//
// The types (Task, Message, Me, ...) are generated from the API's OpenAPI spec
// into api.gen.go. The client in this file adds authentication, retries,
// idempotency keys, timeouts and pagination on top.
//
//	client, err := sinatra.NewClient() // reads SINATRA_TOKEN
//	me, err := client.Me.Get(ctx)
//
// Fields such as Task.Status, Task.Source, Message.Role, Message.Kind and
// Error.Code are plain strings, and the API adds values over time: handle
// values you don't recognise.
//
// Reference: https://docs.sinatra.dev/docs/api-reference
package sinatra

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the API the client talks to unless WithBaseURL says otherwise.
	DefaultBaseURL = "https://api.sinatra.dev"
	// DefaultTimeout is how long each attempt of a request may take, unless
	// WithTimeout says otherwise.
	DefaultTimeout = 60 * time.Second

	modulePath = "github.com/sinatra-dev/sinatra-go"
)

// Client is a Sinatra API client. Its services mirror the API's operations:
// Me.Get is GET /v1/me, Tasks.Create is POST /v1/workspaces/{workspaceId}/tasks.
// A Client is safe for concurrent use.
//
// Requests that are safe to repeat (GETs, the token DELETE, and starts and
// messages, which always carry an Idempotency-Key) are retried up to 3 times
// on 429 and 503. Tasks.Stop is retried on 503 only, Tickets.Delegate never.
// Error responses are returned as *Error.
type Client struct {
	Me       *MeService
	Tasks    *TasksService
	Messages *MessagesService
	Tickets  *TicketsService

	token     string
	baseURL   string // always ends in "/"
	http      *http.Client
	timeout   time.Duration
	userAgent string
	sleep     func(context.Context, time.Duration) error
}

// Option configures NewClient.
type Option func(*Client)

// WithToken sets the API token (sntr_...). Without it, NewClient reads the
// SINATRA_TOKEN environment variable.
func WithToken(token string) Option { return func(c *Client) { c.token = token } }

// WithBaseURL sets the API's address. It defaults to DefaultBaseURL.
func WithBaseURL(baseURL string) Option { return func(c *Client) { c.baseURL = baseURL } }

// WithHTTPClient sets the *http.Client requests go through. It defaults to
// http.DefaultClient.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// WithTimeout sets how long each attempt of a request may take, from sending
// it to reading the whole response, before it fails with an error that wraps
// context.DeadlineExceeded. Timed-out attempts aren't retried. It defaults to
// DefaultTimeout; 0 means no limit. To limit a whole call, retries included,
// give it a context with a deadline.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// NewClient returns a client. It fails if there is no token, from WithToken
// or SINATRA_TOKEN, or if the base URL isn't an absolute http(s) URL.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		baseURL:   DefaultBaseURL,
		http:      http.DefaultClient,
		timeout:   DefaultTimeout,
		userAgent: userAgent(),
		sleep:     sleepContext,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.token == "" {
		c.token = os.Getenv("SINATRA_TOKEN")
	}
	if c.token == "" {
		return nil, errors.New("sinatra: no API token: pass sinatra.WithToken or set SINATRA_TOKEN")
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("sinatra: base URL %q isn't an absolute http(s) URL", c.baseURL)
	}
	// The generated request builders resolve paths against the base URL, so
	// it must end in "/" to keep a path such as https://proxy.example/sinatra.
	if !strings.HasSuffix(c.baseURL, "/") {
		c.baseURL += "/"
	}
	if c.http == nil {
		c.http = http.DefaultClient
	}
	c.Me = &MeService{c}
	c.Tasks = &TasksService{c}
	c.Messages = &MessagesService{c}
	c.Tickets = &TicketsService{c}
	return c, nil
}

// CallOption configures one call of Tasks.Create or Messages.Create.
type CallOption func(*callOptions)

type callOptions struct{ idempotencyKey string }

// WithIdempotencyKey sets the call's Idempotency-Key: 1 to 128 printable ASCII
// characters, with no spaces. Without it, the client makes a random UUID.
// Either way, every retry of the call sends the same key, so a retried start
// never starts a second task. Pass your own key to make a retry safe across
// process restarts too.
func WithIdempotencyKey(key string) CallOption {
	return func(o *callOptions) { o.idempotencyKey = key }
}

func idempotencyKey(opts []CallOption) *string {
	var o callOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.idempotencyKey == "" {
		o.idempotencyKey = newIdempotencyKey()
	}
	return &o.idempotencyKey
}

// Ptr returns a pointer to v, for the optional fields of requests and
// parameters: sinatra.CreateTaskRequest{Prompt: "...", Repo: sinatra.Ptr("acme/web")}.
func Ptr[T any](v T) *T { return &v }

// MeService is the token's user: Client.Me.
type MeService struct{ c *Client }

// Get returns the user the token acts as, and the workspaces they belong to
// (me.get).
func (s *MeService) Get(ctx context.Context) (*Me, error) {
	var out Me
	err := s.c.do(ctx, retryIdempotent, &out, newMeGetRequest)
	return result(&out, err)
}

// RevokeToken revokes the token the client sends (me.revokeToken). Later
// requests with it get 401 invalid_token.
func (s *MeService) RevokeToken(ctx context.Context) error {
	return s.c.do(ctx, retryIdempotent, nil, newMeRevokeTokenRequest)
}

// TasksService is a workspace's tasks: Client.Tasks.
type TasksService struct{ c *Client }

// List returns one page of the workspace's tasks, most recently active first
// (tasks.list). params may be nil. ListAll follows the pages for you.
func (s *TasksService) List(ctx context.Context, workspaceID string, params *TasksListParams) (*TaskList, error) {
	var out TaskList
	err := s.c.do(ctx, retryIdempotent, &out, func(server string) (*http.Request, error) {
		return newTasksListRequest(server, workspaceID, params)
	})
	return result(&out, err)
}

// ListAll returns every task from params.Cursor on (or from the start),
// following nextCursor page by page. It stops after the first error, which
// it yields.
func (s *TasksService) ListAll(ctx context.Context, workspaceID string, params *TasksListParams) iter.Seq2[Task, error] {
	p := TasksListParams{}
	if params != nil {
		p = *params
	}
	return paginate(p.Cursor, func(cursor *string) ([]Task, *string, error) {
		q := p
		q.Cursor = cursor
		page, err := s.List(ctx, workspaceID, &q)
		if err != nil {
			return nil, nil, err
		}
		return page.Tasks, page.NextCursor, nil
	})
}

// Get returns one task, wherever it started (tasks.get).
func (s *TasksService) Get(ctx context.Context, workspaceID, taskID string) (*Task, error) {
	var out Task
	err := s.c.do(ctx, retryIdempotent, &out, func(server string) (*http.Request, error) {
		return newTasksGetRequest(server, workspaceID, taskID)
	})
	return result(&out, err)
}

// Create starts a task (tasks.create). It returns once the task is made, not
// when it's done: follow it with Get and Messages.ListAll. It always sends an
// Idempotency-Key; see WithIdempotencyKey.
func (s *TasksService) Create(ctx context.Context, workspaceID string, body CreateTaskRequest, opts ...CallOption) (*CreateTaskResponse, error) {
	params := &TasksCreateParams{IdempotencyKey: idempotencyKey(opts)}
	var out CreateTaskResponse
	err := s.c.do(ctx, retryIdempotent, &out, func(server string) (*http.Request, error) {
		return newTasksCreateRequest(server, workspaceID, params, body)
	})
	return result(&out, err)
}

// Stop stops the task's running turn (tasks.stop). A task that isn't running
// is 409 not_running. Retried on 503 only.
func (s *TasksService) Stop(ctx context.Context, workspaceID, taskID string) (*StopTaskResponse, error) {
	var out StopTaskResponse
	err := s.c.do(ctx, retryUnavailable, &out, func(server string) (*http.Request, error) {
		return newTasksStopRequest(server, workspaceID, taskID)
	})
	return result(&out, err)
}

// MessagesService is the conversation of a task: Client.Messages.
type MessagesService struct{ c *Client }

// Create sends a follow-up on a task started from the API, CLI, MCP or
// dashboard (messages.create). It always sends an Idempotency-Key; see
// WithIdempotencyKey.
func (s *MessagesService) Create(ctx context.Context, workspaceID, taskID string, body CreateMessageRequest, opts ...CallOption) (*CreateMessageResponse, error) {
	params := &MessagesCreateParams{IdempotencyKey: idempotencyKey(opts)}
	var out CreateMessageResponse
	err := s.c.do(ctx, retryIdempotent, &out, func(server string) (*http.Request, error) {
		return newMessagesCreateRequest(server, workspaceID, taskID, params, body)
	})
	return result(&out, err)
}

// List returns one page of the task's conversation, oldest first
// (messages.list). params may be nil. To wait for a reply, poll with After
// set to the Id of the last entry you read.
func (s *MessagesService) List(ctx context.Context, workspaceID, taskID string, params *MessagesListParams) (*MessageList, error) {
	var out MessageList
	err := s.c.do(ctx, retryIdempotent, &out, func(server string) (*http.Request, error) {
		return newMessagesListRequest(server, workspaceID, taskID, params)
	})
	return result(&out, err)
}

// ListAll returns every entry after params.After (or from the start),
// following next page by page. It stops after the first error, which it
// yields.
func (s *MessagesService) ListAll(ctx context.Context, workspaceID, taskID string, params *MessagesListParams) iter.Seq2[Message, error] {
	p := MessagesListParams{}
	if params != nil {
		p = *params
	}
	return paginate(p.After, func(after *string) ([]Message, *string, error) {
		q := p
		q.After = after
		page, err := s.List(ctx, workspaceID, taskID, &q)
		if err != nil {
			return nil, nil, err
		}
		return page.Messages, page.Next, nil
	})
}

// TicketsService is the issues you could delegate: Client.Tickets.
type TicketsService struct{ c *Client }

// Search returns open Linear and GitHub issues you could delegate
// (tickets.search). params may be nil.
func (s *TicketsService) Search(ctx context.Context, workspaceID string, params *TicketsSearchParams) (*TicketSearchResult, error) {
	var out TicketSearchResult
	err := s.c.do(ctx, retryIdempotent, &out, func(server string) (*http.Request, error) {
		return newTicketsSearchRequest(server, workspaceID, params)
	})
	return result(&out, err)
}

// DelegateTarget is an issue to delegate: a DelegateLinearTicket, a
// DelegateGithubTicket, or a DelegateTicketRequest. Source is filled in for
// you.
type DelegateTarget interface {
	delegateTicketRequest() (DelegateTicketRequest, error)
}

func (t DelegateLinearTicket) delegateTicketRequest() (DelegateTicketRequest, error) {
	var r DelegateTicketRequest
	err := r.FromDelegateLinearTicket(t)
	return r, err
}

func (t DelegateGithubTicket) delegateTicketRequest() (DelegateTicketRequest, error) {
	var r DelegateTicketRequest
	err := r.FromDelegateGithubTicket(t)
	return r, err
}

func (t DelegateTicketRequest) delegateTicketRequest() (DelegateTicketRequest, error) { return t, nil }

// Delegate hands Sinatra an existing issue, as you (tickets.delegate):
//
//	client.Tickets.Delegate(ctx, workspaceID, sinatra.DelegateLinearTicket{Id: "ENG-123"})
//	client.Tickets.Delegate(ctx, workspaceID, sinatra.DelegateGithubTicket{Repo: "acme/web", Number: 42})
//
// Never retried: the operation takes no Idempotency-Key.
func (s *TicketsService) Delegate(ctx context.Context, workspaceID string, ticket DelegateTarget) (*DelegateTicketResponse, error) {
	if ticket == nil {
		return nil, errors.New("sinatra: Delegate needs a ticket")
	}
	body, err := ticket.delegateTicketRequest()
	if err != nil {
		return nil, fmt.Errorf("sinatra: encoding the ticket: %w", err)
	}
	var out DelegateTicketResponse
	err = s.c.do(ctx, retryNever, &out, func(server string) (*http.Request, error) {
		return newTicketsDelegateRequest(server, workspaceID, body)
	})
	return result(&out, err)
}

// paginate yields the items of each page, fetching the next with the cursor
// the last one returned, until a page has no cursor or a fetch fails.
func paginate[T any](start *string, fetch func(cursor *string) ([]T, *string, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		cursor := start
		for {
			items, next, err := fetch(cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if next == nil || *next == "" {
				return
			}
			cursor = next
		}
	}
}

func result[T any](out *T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	return out, nil
}

// userAgent is sinatra-go/<module version>, when the build records one.
func userAgent() string {
	version := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path == modulePath {
			version = info.Main.Version
		}
		for _, dep := range info.Deps {
			if dep.Path == modulePath {
				version = dep.Version
			}
		}
	}
	if version == "" || version == "(devel)" {
		return "sinatra-go"
	}
	return "sinatra-go/" + version
}
