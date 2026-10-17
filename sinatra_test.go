package sinatra

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const ws = "ws_1"

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want a *sinatra.Error, got %T: %v", err, err)
	}
	return e
}

// --- retries ---

func TestRetriesReuseTheIdempotencyKey(t *testing.T) {
	s := newStub(t,
		reply{status: 503, body: map[string]any{"error": "unavailable", "taskId": "task_1", "messageId": "m"}},
		reply{status: 202, body: map[string]any{"task": taskJSON(nil), "messageId": "m"}},
	)
	c, _ := newTestClient(t, s)

	created, err := c.Tasks.Create(context.Background(), ws, CreateTaskRequest{Prompt: "Fix the flaky test"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.Id != "task_1" {
		t.Errorf("task id = %q", created.Task.Id)
	}
	got := s.requests()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	first, second := got[0].header.Get("Idempotency-Key"), got[1].header.Get("Idempotency-Key")
	if !uuidV4.MatchString(first) {
		t.Errorf("key %q isn't a UUIDv4", first)
	}
	if second != first {
		t.Errorf("retry sent key %q, first attempt sent %q", second, first)
	}
}

func TestAKeyYouPassIsSentOnEveryAttempt(t *testing.T) {
	s := newStub(t,
		reply{status: 503, body: map[string]any{"error": "unavailable"}},
		reply{status: 202, body: map[string]any{"messageId": "mine-1"}},
	)
	c, _ := newTestClient(t, s)

	_, err := c.Messages.Create(context.Background(), ws, "task_1", CreateMessageRequest{Text: "and the docs"}, WithIdempotencyKey("mine-1"))
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range s.requests() {
		if k := r.header.Get("Idempotency-Key"); k != "mine-1" {
			t.Errorf("attempt %d sent key %q", i+1, k)
		}
	}
}

func TestEachCallGetsItsOwnKey(t *testing.T) {
	s := newStub(t,
		reply{status: 202, body: map[string]any{"messageId": "a"}},
		reply{status: 202, body: map[string]any{"messageId": "b"}},
	)
	c, _ := newTestClient(t, s)
	ctx := context.Background()

	for _, text := range []string{"one", "two"} {
		if _, err := c.Messages.Create(ctx, ws, "task_1", CreateMessageRequest{Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.requests()
	a, b := got[0].header.Get("Idempotency-Key"), got[1].header.Get("Idempotency-Key")
	if !uuidV4.MatchString(a) || !uuidV4.MatchString(b) || a == b {
		t.Errorf("keys %q and %q: want two different UUIDv4s", a, b)
	}
}

func Test429HonoursRetryAfter(t *testing.T) {
	s := newStub(t,
		reply{status: 429, header: map[string]string{"Retry-After": "1"}, body: map[string]any{"error": "rate_limited"}},
		reply{status: 200, body: meJSON},
	)
	c, clock := newTestClient(t, s)

	me, err := c.Me.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.User.Id != "user_1" {
		t.Errorf("user id = %q", me.User.Id)
	}
	waits := clock.waits()
	if len(waits) != 1 || waits[0] < time.Second || waits[0] >= 1500*time.Millisecond {
		t.Fatalf("waits = %v, want one wait of 1s before the retry", waits)
	}
	if clock.requestsBefore[0] != 1 || len(s.requests()) != 2 {
		t.Errorf("the second request must come after the wait: %d requests before it, %d in all", clock.requestsBefore[0], len(s.requests()))
	}
}

func TestRetryAfterIsCappedAt60Seconds(t *testing.T) {
	s := newStub(t,
		reply{status: 429, header: map[string]string{"Retry-After": "3600"}, body: map[string]any{"error": "rate_limited"}},
		reply{status: 200, body: meJSON},
	)
	c, clock := newTestClient(t, s)

	if _, err := c.Me.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waits := clock.waits(); len(waits) != 1 || waits[0] != time.Minute {
		t.Errorf("waits = %v, want [1m0s]", waits)
	}
}

func Test503BacksOffAndGivesUpAfter3Retries(t *testing.T) {
	var replies []reply
	for range 4 {
		replies = append(replies, reply{status: 503, body: map[string]any{"error": "unavailable"}})
	}
	s := newStub(t, replies...)
	c, clock := newTestClient(t, s)

	_, err := c.Tasks.Get(context.Background(), ws, "task_1")
	if e := asError(t, err); e.Status != 503 || e.Code != "unavailable" {
		t.Errorf("error = %d %q, want 503 unavailable", e.Status, e.Code)
	}
	if n := len(s.requests()); n != 4 {
		t.Errorf("requests = %d, want 4 (1 + 3 retries)", n)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	if got := clock.waits(); !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}
}

func Test500IsNotRetried(t *testing.T) {
	s := newStub(t, reply{status: 500, body: map[string]any{"error": "internal_error"}})
	c, _ := newTestClient(t, s)

	_, err := c.Tasks.Stop(context.Background(), ws, "task_1")
	if e := asError(t, err); e.Status != 500 {
		t.Errorf("status = %d, want 500", e.Status)
	}
	if n := len(s.requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestTasksStopRetries503ButNot429(t *testing.T) {
	s := newStub(t,
		reply{status: 503, body: map[string]any{"error": "unavailable"}},
		reply{status: 200, body: map[string]any{"accepted": true}},
	)
	c, _ := newTestClient(t, s)
	if _, err := c.Tasks.Stop(context.Background(), ws, "task_1"); err != nil {
		t.Fatalf("503 then 200: %v", err)
	}

	s = newStub(t, reply{status: 429, header: map[string]string{"Retry-After": "1"}, body: map[string]any{"error": "rate_limited"}})
	c, _ = newTestClient(t, s)
	_, err := c.Tasks.Stop(context.Background(), ws, "task_1")
	if e := asError(t, err); e.Status != 429 {
		t.Errorf("status = %d, want 429", e.Status)
	}
	if n := len(s.requests()); n != 1 {
		t.Errorf("429 on stop: requests = %d, want 1", n)
	}
}

func TestDelegateIsNotRetried(t *testing.T) {
	s := newStub(t, reply{status: 503, body: map[string]any{"error": "unavailable"}})
	c, _ := newTestClient(t, s)

	_, err := c.Tickets.Delegate(context.Background(), ws, DelegateLinearTicket{Id: "ENG-1"})
	if e := asError(t, err); e.Status != 503 {
		t.Errorf("status = %d, want 503", e.Status)
	}
	if n := len(s.requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestCancellingTheContextStopsARetryWait(t *testing.T) {
	s := newStub(t, reply{status: 503, body: map[string]any{"error": "unavailable"}})
	c, _ := newTestClient(t, s)
	c.sleep = sleepContext // the real clock

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.Tasks.Get(ctx, ws, "task_1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if waited := time.Since(start); waited > 400*time.Millisecond {
		t.Errorf("returned after %v; the 0.5s retry wait should have been cut short", waited)
	}
}

// --- timeouts ---

func TestAHungRequestTimesOut(t *testing.T) {
	s := newStub(t, reply{hang: true})
	c, _ := newTestClient(t, s, WithTimeout(100*time.Millisecond))

	start := time.Now()
	_, err := c.Tasks.Get(context.Background(), ws, "task_1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want one that wraps context.DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %v, want about the 100ms timeout", took)
	}
	if n := len(s.requests()); n != 1 {
		t.Errorf("requests = %d, want 1: a timeout isn't retried", n)
	}
}

func TestACallersDeadlineIsReportedAsTheCallers(t *testing.T) {
	s := newStub(t, reply{hang: true})
	c, _ := newTestClient(t, s, WithTimeout(time.Minute))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Tasks.Get(ctx, ws, "task_1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want one that wraps context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), "client's") {
		t.Errorf("err = %q: the caller's deadline passed, not the client's 1m timeout", err)
	}
}

func TestTheClientTimeoutIsReportedAsTheClients(t *testing.T) {
	s := newStub(t, reply{hang: true})
	c, _ := newTestClient(t, s, WithTimeout(100*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := c.Tasks.Get(ctx, ws, "task_1")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "client's 100ms timeout") {
		t.Errorf("err = %v, want the client's 100ms timeout, wrapping context.DeadlineExceeded", err)
	}
}

// --- decoding ---

func TestUnknownStatusDecodes(t *testing.T) {
	s := newStub(t, reply{status: 200, body: taskJSON(map[string]any{"status": "teleporting", "source": "carrier_pigeon"})})
	c, _ := newTestClient(t, s)

	task, err := c.Tasks.Get(context.Background(), ws, "task_1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "teleporting" || task.Source != "carrier_pigeon" {
		t.Errorf("status, source = %q, %q", task.Status, task.Source)
	}
}

func TestUnknownErrorCodeSurfaces(t *testing.T) {
	s := newStub(t, reply{status: 409, body: map[string]any{"error": "brand_new", "reason": "x", "requestId": "req_1"}})
	c, _ := newTestClient(t, s)

	_, err := c.Tasks.Create(context.Background(), ws, CreateTaskRequest{Prompt: "p"})
	e := asError(t, err)
	if e.Status != 409 || e.Code != "brand_new" || e.Reason != "x" || e.RequestID != "req_1" {
		t.Errorf("error = %+v", e)
	}
	if e.Details == nil || e.Details.Error != "brand_new" {
		t.Errorf("details = %+v", e.Details)
	}
	if got := e.Error(); got != "sinatra: 409 brand_new: x" {
		t.Errorf("Error() = %q", got)
	}
}

func TestAnErrorWithoutAJSONBodyStillSurfacesItsStatus(t *testing.T) {
	s := newStub(t, reply{status: 502, header: map[string]string{"x-request-id": "req_hdr"}, body: "<html>Bad gateway</html>"})
	c, _ := newTestClient(t, s)

	_, err := c.Me.Get(context.Background())
	e := asError(t, err)
	if e.Status != 502 || e.Code != "http_error" || e.RequestID != "req_hdr" || string(e.Body) != "<html>Bad gateway</html>" {
		t.Errorf("error = %d %q %q %q", e.Status, e.Code, e.RequestID, e.Body)
	}
	if e.Details != nil {
		t.Errorf("details = %+v, want nil for a non-JSON body", e.Details)
	}
}

func TestA2xxBodyThatDoesNotDecodeIsAnError(t *testing.T) {
	s := newStub(t, reply{status: 200, body: "not json"})
	c, _ := newTestClient(t, s)

	task, err := c.Tasks.Get(context.Background(), ws, "task_1")
	if err == nil {
		t.Fatalf("got task %+v and no error", task)
	}
}

// --- requests ---

func TestBodylessRequestsSendNoContentType(t *testing.T) {
	s := newStub(t,
		reply{status: 200, body: map[string]any{"accepted": true}},
		reply{status: 204},
	)
	c, _ := newTestClient(t, s)
	ctx := context.Background()

	if _, err := c.Tasks.Stop(ctx, ws, "task_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Me.RevokeToken(ctx); err != nil {
		t.Fatal(err)
	}
	got := s.requests()
	if got[0].method != "POST" || got[0].path != "/v1/workspaces/ws_1/tasks/task_1/stop" {
		t.Errorf("stop sent %s %s", got[0].method, got[0].path)
	}
	if got[1].method != "DELETE" || got[1].path != "/v1/me/token" {
		t.Errorf("revoke sent %s %s", got[1].method, got[1].path)
	}
	for _, r := range got {
		if ct, ok := r.header["Content-Type"]; ok {
			t.Errorf("%s %s sent Content-Type %q", r.method, r.path, ct)
		}
		if len(r.body) != 0 {
			t.Errorf("%s %s sent a body: %q", r.method, r.path, r.body)
		}
	}
}

func TestRequestsWithABodySendJSON(t *testing.T) {
	s := newStub(t,
		reply{status: 202, body: map[string]any{"task": taskJSON(nil), "messageId": "m"}},
		reply{status: 200, body: map[string]any{"ok": true}},
	)
	c, _ := newTestClient(t, s)
	ctx := context.Background()

	if _, err := c.Tasks.Create(ctx, ws, CreateTaskRequest{Prompt: "Fix it", Repo: Ptr("acme/web")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tickets.Delegate(ctx, ws, DelegateGithubTicket{Repo: "acme/web", Number: 42}); err != nil {
		t.Fatal(err)
	}
	got := s.requests()
	wantBodies := []map[string]any{
		{"prompt": "Fix it", "repo": "acme/web"},
		{"source": "github", "repo": "acme/web", "number": float64(42)},
	}
	for i, r := range got {
		if ct := r.header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("request %d Content-Type = %q", i, ct)
		}
		var body map[string]any
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("request %d body %q: %v", i, r.body, err)
		}
		if !equalJSON(body, wantBodies[i]) {
			t.Errorf("request %d body = %v, want %v", i, body, wantBodies[i])
		}
		if a := r.header.Get("Authorization"); a != "Bearer sntr_test" {
			t.Errorf("request %d Authorization = %q", i, a)
		}
	}
}

func TestTheTokenDefaultsToSINATRA_TOKEN(t *testing.T) {
	t.Setenv("SINATRA_TOKEN", "sntr_from_env")
	s := newStub(t, reply{status: 200, body: meJSON})
	c, err := NewClient(WithBaseURL(s.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := s.requests()[0].header.Get("Authorization"); a != "Bearer sntr_from_env" {
		t.Errorf("Authorization = %q", a)
	}
}

func TestNoTokenIsAnErrorBeforeAnyRequest(t *testing.T) {
	t.Setenv("SINATRA_TOKEN", "")
	c, err := NewClient()
	if err == nil {
		t.Fatalf("NewClient() = %v, nil; want an error", c)
	}
}

func TestTheDefaultBaseURL(t *testing.T) {
	c, err := NewClient(WithToken("sntr_test"))
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "https://api.sinatra.dev/" {
		t.Errorf("base URL = %q", c.baseURL)
	}
}

func TestABaseURLWithAPathKeepsIt(t *testing.T) {
	s := newStub(t, reply{status: 200, body: meJSON})
	c, err := NewClient(WithToken("sntr_test"), WithBaseURL(s.server.URL+"/proxy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := s.requests()[0].path; p != "/proxy/v1/me" {
		t.Errorf("path = %q, want /proxy/v1/me", p)
	}
}

// --- pagination ---

func TestListIteratorFollowsNextCursor(t *testing.T) {
	s := newStub(t,
		reply{status: 200, body: map[string]any{"tasks": []any{taskJSON(map[string]any{"id": "t1"}), taskJSON(map[string]any{"id": "t2"})}, "nextCursor": "c2"}},
		reply{status: 200, body: map[string]any{"tasks": []any{taskJSON(map[string]any{"id": "t3"})}, "nextCursor": nil}},
	)
	c, _ := newTestClient(t, s)

	var ids []string
	for task, err := range c.Tasks.ListAll(context.Background(), ws, &TasksListParams{Status: Ptr("pr_open")}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.Id)
	}
	if !slices.Equal(ids, []string{"t1", "t2", "t3"}) {
		t.Errorf("ids = %v", ids)
	}
	got := s.requests()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	first, _ := url.ParseQuery(got[0].rawQuery)
	second, _ := url.ParseQuery(got[1].rawQuery)
	if first.Has("cursor") || first.Get("status") != "pr_open" {
		t.Errorf("page 1 query = %q", got[0].rawQuery)
	}
	if second.Get("cursor") != "c2" || second.Get("status") != "pr_open" {
		t.Errorf("page 2 query = %q", got[1].rawQuery)
	}
}

func TestMessageIteratorFollowsNextAsAfter(t *testing.T) {
	s := newStub(t,
		reply{status: 200, body: map[string]any{"messages": []any{messageJSON("m1")}, "next": "m1"}},
		reply{status: 200, body: map[string]any{"messages": []any{messageJSON("m2")}, "next": nil}},
	)
	c, _ := newTestClient(t, s)

	var ids []string
	for m, err := range c.Messages.ListAll(context.Background(), ws, "task_1", nil) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.Id)
	}
	if !slices.Equal(ids, []string{"m1", "m2"}) {
		t.Errorf("ids = %v", ids)
	}
	if q := s.requests()[1].rawQuery; q != "after=m1" {
		t.Errorf("page 2 query = %q, want after=m1", q)
	}
}

func TestAnIteratorStopsAtAnError(t *testing.T) {
	s := newStub(t,
		reply{status: 200, body: map[string]any{"tasks": []any{taskJSON(nil)}, "nextCursor": "c2"}},
		reply{status: 400, body: map[string]any{"error": "invalid_cursor"}},
	)
	c, _ := newTestClient(t, s)

	var items, errs int
	var last error
	for _, err := range c.Tasks.ListAll(context.Background(), ws, nil) {
		if err != nil {
			errs++
			last = err
			continue
		}
		items++
	}
	if items != 1 || errs != 1 || asError(t, last).Code != "invalid_cursor" {
		t.Errorf("items = %d, errors = %d (%v)", items, errs, last)
	}
}

// --- helpers ---

func equalJSON(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
