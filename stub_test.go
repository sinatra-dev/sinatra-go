package sinatra

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// reply is one scripted answer of the stub server.
type reply struct {
	status int
	header map[string]string
	// body is written as is when it's a string, and as JSON otherwise.
	body any
	// hang never answers: the handler waits until the client gives up.
	hang bool
}

// request is what the stub server received.
type request struct {
	method   string
	path     string
	rawQuery string
	header   http.Header
	body     []byte
}

// stub is a scripted HTTP server that records every request. Replies are
// used in order; a request beyond the script fails the test.
type stub struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	replies []reply
	got     []request
}

func newStub(t *testing.T, replies ...reply) *stub {
	t.Helper()
	s := &stub{t: t, replies: replies}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.got = append(s.got, request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
	n := len(s.got)
	var rep reply
	ok := n <= len(s.replies)
	if ok {
		rep = s.replies[n-1]
	}
	s.mu.Unlock()

	if !ok {
		s.t.Errorf("unexpected request %d: %s %s", n, r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
		return
	}
	if rep.hang {
		<-r.Context().Done()
		return
	}
	for k, v := range rep.header {
		w.Header().Set(k, v)
	}
	var out []byte
	switch b := rep.body.(type) {
	case nil:
	case string:
		out = []byte(b)
	default:
		out, _ = json.Marshal(b)
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(rep.status)
	_, _ = w.Write(out)
}

func (s *stub) requests() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request(nil), s.got...)
}

// sleeps records the waits the client asked for, without waiting: a fake clock.
type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
	// requestsBefore is how many requests the stub had seen at each wait.
	requestsBefore []int
	stub           *stub
}

func (f *sleeps) sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.d = append(f.d, d)
	f.requestsBefore = append(f.requestsBefore, len(f.stub.requests()))
	return ctx.Err()
}

func (f *sleeps) waits() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.d...)
}

// newTestClient returns a client for the stub, with a fake clock.
func newTestClient(t *testing.T, s *stub, opts ...Option) (*Client, *sleeps) {
	t.Helper()
	c, err := NewClient(append([]Option{WithToken("sntr_test"), WithBaseURL(s.server.URL)}, opts...)...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	clock := &sleeps{stub: s}
	c.sleep = clock.sleep
	return c, clock
}

func taskJSON(overrides map[string]any) map[string]any {
	t := map[string]any{
		"id":             "task_1",
		"status":         "implementing",
		"source":         "api",
		"title":          "Fix the flaky test",
		"repo":           "acme/web",
		"pullRequest":    nil,
		"queued":         false,
		"createdAt":      "2026-10-16T12:00:00.000Z",
		"lastActivityAt": nil,
		"url":            "https://app.sinatra.dev/tasks/task_1",
	}
	for k, v := range overrides {
		t[k] = v
	}
	return t
}

func messageJSON(id string) map[string]any {
	return map[string]any{
		"id":        id,
		"messageId": nil,
		"role":      "agent",
		"kind":      "response",
		"body":      "entry " + id,
		"createdAt": "2026-10-16T12:00:00.000Z",
	}
}

var meJSON = map[string]any{
	"user":       map[string]any{"id": "user_1", "githubLogin": "octo", "name": "Octo"},
	"workspaces": []any{map[string]any{"id": "ws_1", "name": "Acme", "role": "owner"}},
}
