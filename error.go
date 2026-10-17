package sinatra

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Error is the error every method returns for an HTTP error response. Get it
// with errors.As:
//
//	var apiErr *sinatra.Error
//	if errors.As(err, &apiErr) && apiErr.Code == "not_running" { ... }
//
// Network failures, timeouts and cancellations are not an *Error: they are
// returned as net/http and context report them.
type Error struct {
	// Status is the HTTP status, such as 409.
	Status int
	// Code is the API's machine-readable error code, such as "capacity_full".
	// New codes can appear at any time, so treat it as an open string. It is
	// "http_error" when the response carried no code, such as an HTML error
	// page from a proxy. Branch on Code, not on Reason.
	Code string
	// Reason is a sentence saying what went wrong, written for a person.
	Reason string
	// RequestID is the request's id, from the body or the x-request-id
	// header. Include it when you contact support.
	RequestID string
	// Body is the raw response body.
	Body []byte
	// Header holds the response headers.
	Header http.Header
	// Details is the decoded body when it was a JSON object, with fields such
	// as BillingUrl on some refusals. Nil otherwise.
	Details *ErrorBody
}

func (e *Error) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("sinatra: %d %s: %s", e.Status, e.Code, e.Reason)
	}
	return fmt.Sprintf("sinatra: %d %s", e.Status, e.Code)
}

func newError(status int, header http.Header, body []byte) *Error {
	e := &Error{Status: status, Code: "http_error", Body: body, Header: header}
	var details ErrorBody
	if json.Unmarshal(body, &details) == nil && isJSONObject(body) {
		e.Details = &details
		if details.Error != "" {
			e.Code = details.Error
		}
		if details.Reason != nil {
			e.Reason = *details.Reason
		}
		if details.RequestId != nil {
			e.RequestID = *details.RequestId
		}
	}
	if e.RequestID == "" {
		e.RequestID = header.Get("X-Request-Id")
	}
	return e
}

func isJSONObject(body []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(body, &m) == nil && m != nil
}
