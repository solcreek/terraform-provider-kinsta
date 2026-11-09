// SPDX-License-Identifier: MPL-2.0

package kinsta

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New("key", WithBaseURL(srv.URL), WithPollInterval(time.Millisecond))
}

func TestRetriesRateLimits(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			// Kinsta sometimes throttles with another status and a message.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":400,"message":"Too many requests, slow down"}`))
		default:
			_, _ = w.Write([]byte(`{"name":"k","company":"c-1","status":"active"}`))
		}
	})
	c.maxRetries, c.retryBase = 3, time.Millisecond
	info, err := c.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Company != "c-1" || calls.Load() != 3 {
		t.Fatalf("company %q after %d calls", info.Company, calls.Load())
	}
}

func TestErrorMessages(t *testing.T) {
	cases := map[int]string{
		401: "invalid, expired or revoked",
		403: "lacks the access level",
		404: "The requested site was not found!",
	}
	for status, want := range cases {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer key" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"status":` + strconv.Itoa(status) + `,"message":"The requested site was not found!"}`))
		})
		_, err := c.GetSite(context.Background(), "s")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%d: error %v, want it to contain %q", status, err, want)
		}
		if status == 404 && !IsNotFound(err) {
			t.Errorf("404 not reported as not found: %v", err)
		}
	}
}

func TestErrorDataMessage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"status":400,"message":"Bad request","data":{"message":"display_name is taken"}}`))
	})
	_, err := c.CreatePlainSite(context.Background(), CreatePlainSiteRequest{})
	if err == nil || !strings.Contains(err.Error(), "Bad request: display_name is taken") {
		t.Fatalf("error %v", err)
	}
}

func TestWaitOperation(t *testing.T) {
	var polls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch n := polls.Add(1); {
		case n == 1:
			// A fresh operation can 404 briefly.
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"status":404,"message":"Operation not found"}`))
		case n < 4:
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"status":202,"message":"Operation in progress"}`))
		default:
			_, _ = w.Write([]byte(`{"status":200,"message":"Successfully finished request.","data":{"idSite":"site-9"}}`))
		}
	})
	op, err := c.WaitOperation(context.Background(), "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := op.DataString("idSite"); got != "site-9" {
		t.Fatalf("idSite = %q", got)
	}
}

func TestWaitOperationFailure(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"status":500,"message":"Operation failed!","data":{"message":"Domain is already in use"}}`))
	})
	_, err := c.WaitOperation(context.Background(), "op-1")
	var oe *OperationError
	if !errors.As(err, &oe) || oe.Message != "Domain is already in use" {
		t.Fatalf("error %v", err)
	}
}

func TestWaitOperationDeadline(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"status":202,"message":"Operation in progress"}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.WaitOperation(ctx, "op-1")
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("error %v", err)
	}
}

func TestDNSWritesWithoutOperation(t *testing.T) {
	var methods []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"status":202,"message":"DNS record created"}`))
	})
	if err := c.CreateRecord(context.Background(), "z", Record{Type: "A", Name: "a.example.com", TTL: 300}); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0] != "POST /domains/z/dns-records" {
		t.Fatalf("requests %v: a 202 without operation id must not be polled", methods)
	}
}

func TestEncodeDecodeValue(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := []struct{ typ, in, stored string }{
		{"TXT", "v=spf1 -all", `"v=spf1 -all"`},
		{"TXT", `"already quoted"`, `"already quoted"`},
		{"TXT", `say "hi"`, `"say \"hi\""`},
		{"TXT", long, `"` + long[:255] + `" "` + long[255:] + `"`},
		{"SPF", "v=spf1 -all", `"v=spf1 -all"`},
		{"CNAME", "target.example.com.", "target.example.com."},
		{"MX", "10 mail.example.com", "10 mail.example.com"},
	}
	for _, tc := range cases {
		if got := EncodeValue(tc.typ, tc.in); got != tc.stored {
			t.Errorf("EncodeValue(%s, %q) = %q, want %q", tc.typ, tc.in, got, tc.stored)
		}
		want := tc.in
		if strings.HasPrefix(tc.in, `"`) {
			want = strings.Trim(tc.in, `"`)
		}
		if got := DecodeValue(tc.typ, tc.stored); got != want {
			t.Errorf("DecodeValue(%s, %q) = %q, want %q", tc.typ, tc.stored, got, want)
		}
	}
	// Strings split somewhere other than 255 characters do not round-trip.
	if got := DecodeValue("TXT", `"ab" "cd"`); got != `"ab" "cd"` {
		t.Errorf("DecodeValue kept a non-canonical split: %q", got)
	}
}

func TestPHPVersion(t *testing.T) {
	v := "php8.3"
	e := Environment{ContainerInfo: &ContainerInfo{PHPEngineVersion: v}}
	if e.PHPVersion() != "8.3" {
		t.Fatalf("PHPVersion = %q", e.PHPVersion())
	}
}
