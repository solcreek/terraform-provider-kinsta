// SPDX-License-Identifier: MPL-2.0

// Package kinsta is a minimal, dependency-free (stdlib only) client for the
// subset of the Kinsta API v2 (https://api-docs.kinsta.com) that this
// Terraform provider needs: WordPress sites and environments, site domains,
// and Kinsta DNS zones and records.
//
// Kinsta answers most writes with 202 and an operation id. The client polls
// those operations to completion (see WaitOperation) so that resources only
// report success once the change has actually landed.
package kinsta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the production Kinsta API.
const DefaultBaseURL = "https://api.kinsta.com/v2"

// Client is a Kinsta API v2 client. It is safe for concurrent use.
type Client struct {
	http         *http.Client
	baseURL      string
	apiKey       string
	userAgent    string
	pollInterval time.Duration
	maxRetries   int
	retryBase    time.Duration

	// Kinsta throttles bursts of parallel calls hard, and Terraform runs up to
	// ten operations at once. Requests are therefore sent one at a time; the
	// lock is held only for the HTTP round trip, never while polling sleeps.
	mu sync.Mutex
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (mainly for tests).
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithTimeout sets the per-request HTTP timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.http.Timeout = d
		}
	}
}

// WithHTTPClient injects a custom *http.Client (mainly for tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithPollInterval sets how often operations are polled (mainly for tests).
func WithPollInterval(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.pollInterval = d
		}
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// New builds a Client authenticated with a Kinsta API key.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		http:         &http.Client{Timeout: 60 * time.Second},
		baseURL:      DefaultBaseURL,
		apiKey:       apiKey,
		userAgent:    "terraform-provider-kinsta",
		pollInterval: 5 * time.Second,
		maxRetries:   5,
		retryBase:    time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is returned for any non-2xx response.
type APIError struct {
	StatusCode int
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("kinsta API error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("kinsta API error %d: %s", e.StatusCode, strings.TrimSpace(e.Body))
}

// IsNotFound reports whether err is an API 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound
}

// StatusCode returns the HTTP status of an APIError, or 0.
func StatusCode(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.StatusCode
	}
	return 0
}

// errorEnvelope matches Kinsta's error body: {"status": 404, "message": "..."}.
type errorEnvelope struct {
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// isRateLimited reports a throttled response. Besides 429, Kinsta sometimes
// signals throttling only through the message text.
func isRateLimited(status int, body []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if status < 400 {
		return false
	}
	var env errorEnvelope
	return json.Unmarshal(body, &env) == nil && strings.Contains(strings.ToLower(env.Message), "too many requests")
}

// rawResponse is one HTTP exchange, before the status is interpreted.
type rawResponse struct {
	status int
	body   []byte
}

// send performs a request with retries on rate limiting and returns the raw
// response whatever its status.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any) (*rawResponse, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		payload = b
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		c.mu.Lock()
		resp, err := c.http.Do(req)
		var respBody []byte
		if err == nil {
			respBody, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		c.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, path, err)
		}

		if isRateLimited(resp.StatusCode, respBody) && attempt < c.maxRetries {
			wait := retryAfter(resp.Header.Get("Retry-After"), attempt, c.retryBase)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		return &rawResponse{status: resp.StatusCode, body: respBody}, nil
	}
}

// do performs a request and decodes a 2xx response into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	raw, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	if raw.status < 200 || raw.status > 299 {
		return parseError(raw.status, raw.body)
	}
	if out != nil && len(raw.body) > 0 {
		if err := json.Unmarshal(raw.body, out); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func parseError(status int, body []byte) error {
	ae := &APIError{StatusCode: status, Body: string(body)}
	var env errorEnvelope
	if json.Unmarshal(body, &env) == nil {
		ae.Message = env.Message
		if detail := dataMessage(env.Data); detail != "" && detail != env.Message {
			ae.Message = strings.TrimSpace(ae.Message + ": " + detail)
		}
	}
	// Make credential problems actionable.
	switch status {
	case http.StatusUnauthorized:
		ae.Message = strings.TrimSpace(ae.Message + " (the API key is missing, invalid, expired or revoked — check KINSTA_API_KEY)")
	case http.StatusForbidden:
		ae.Message = strings.TrimSpace(ae.Message + " (the API key lacks the access level this call needs, or the resource belongs to another company)")
	}
	return ae
}

// dataMessage extracts a human-readable reason from an error's `data` field,
// which Kinsta fills with either a string or an object carrying `message`.
func dataMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Message
	}
	return ""
}

func retryAfter(header string, attempt int, base time.Duration) time.Duration {
	if header != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	// Exponential fallback: 1s, 2s, 4s, 8s, 16s.
	return time.Duration(1<<attempt) * base
}
