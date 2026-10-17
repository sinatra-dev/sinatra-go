package sinatra

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// maxRetries is how many times a request is retried after the first attempt.
	maxRetries = 3
	// firstBackoff is the first 503 wait; it doubles on each retry (0.5s, 1s, 2s).
	firstBackoff = 500 * time.Millisecond
	// maxRetryAfter caps the wait a 429's Retry-After asks for.
	maxRetryAfter = time.Minute
)

// retryOn says which statuses a request may be retried on.
type retryOn struct{ tooManyRequests, unavailable bool }

var (
	// retryIdempotent is for GETs, the token DELETE, and POSTs that carry an
	// Idempotency-Key.
	retryIdempotent = retryOn{tooManyRequests: true, unavailable: true}
	// retryUnavailable is for tasks.stop: a stop is safe to repeat after a
	// 503, but not after a 429.
	retryUnavailable = retryOn{unavailable: true}
	// retryNever is for POSTs without an Idempotency-Key.
	retryNever = retryOn{}
)

func (r retryOn) allows(status int) bool {
	return (status == http.StatusTooManyRequests && r.tooManyRequests) ||
		(status == http.StatusServiceUnavailable && r.unavailable)
}

// do sends a request until it succeeds, fails with a status policy doesn't
// retry, or runs out of retries, and decodes a 2xx body into out (unless out
// is nil). build makes each attempt's request; it must make the same request
// every time, so callers compute an Idempotency-Key once, outside it.
func (c *Client) do(ctx context.Context, policy retryOn, out any, build func(server string) (*http.Request, error)) error {
	for retry := 0; ; retry++ {
		req, err := build(c.baseURL)
		if err != nil {
			return fmt.Errorf("sinatra: building the request: %w", err)
		}
		status, header, body, err := c.attempt(ctx, req)
		if err != nil {
			return err
		}
		if status >= 200 && status < 300 {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("sinatra: decoding the %d response to %s %s: %w", status, req.Method, req.URL.Path, err)
			}
			return nil
		}
		if retry < maxRetries && policy.allows(status) {
			if err := c.sleep(ctx, retryDelay(status, header, retry)); err != nil {
				return err
			}
			continue
		}
		return newError(status, header, body)
	}
}

// attempt sends req once, within the client's timeout, and reads the whole
// response.
func (c *Client) attempt(parent context.Context, req *http.Request) (int, http.Header, []byte, error) {
	ctx := parent
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, c.timeout)
		defer cancel()
	}
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, c.attemptError(parent, ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, c.attemptError(parent, ctx, err)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// attemptError says when an attempt failed because the client's timeout
// passed. When the caller's own context ended (its deadline, or a cancel),
// the error is returned as net/http reports it. Both kinds of deadline wrap
// context.DeadlineExceeded.
func (c *Client) attemptError(parent, ctx context.Context, err error) error {
	if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("sinatra: no response within the client's %s timeout: %w", c.timeout, err)
	}
	return err
}

// retryDelay is how long to wait before retry number retry (0 for the first).
func retryDelay(status int, header http.Header, retry int) time.Duration {
	if status == http.StatusTooManyRequests {
		if d, ok := parseRetryAfter(header.Get("Retry-After")); ok {
			return min(d, maxRetryAfter)
		}
	}
	return firstBackoff << retry
}

// parseRetryAfter reads a Retry-After header: seconds, or an HTTP date.
func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		if math.IsNaN(s) || s < 0 {
			return 0, true
		}
		if s >= maxRetryAfter.Seconds() {
			return maxRetryAfter, true
		}
		return time.Duration(s * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0), true
	}
	return 0, false
}

// sleepContext waits for d, or until ctx is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// newIdempotencyKey returns a random UUIDv4.
func newIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go 1.24)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
