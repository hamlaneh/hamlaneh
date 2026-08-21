package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
)

// readinessStub implements httpserver.ReadinessChecker with a canned result
// and records whether the probe context carried a deadline.
type readinessStub struct {
	err         error
	gotDeadline bool
}

func (s *readinessStub) Ready(ctx context.Context) error {
	_, s.gotDeadline = ctx.Deadline()
	return s.err
}

// TestNotImplementedStubs pins every not-yet-implemented contract endpoint
// to 501 with the contract's Error schema (decoded through the generated
// api.Error type, so the stub body cannot drift from the spec).
func TestNotImplementedStubs(t *testing.T) {
	t.Parallel()

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/api/v1/auth/refresh"},
		{http.MethodGet, "/api/v1/users/me"},
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodPost, "/api/v1/admin/users"},
	}

	handler := httpserver.Handler(nil)
	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(ep.method, ep.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotImplemented {
				t.Errorf("got status %d, want %d", rec.Code, http.StatusNotImplemented)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("got Content-Type %q, want application/json", ct)
			}

			var body api.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not the contract Error shape: %v", rec.Body.String(), err)
			}
			if body.Error.Code != "not_implemented" {
				t.Errorf("got error code %q, want %q", body.Error.Code, "not_implemented")
			}
			if body.Error.Message != "endpoint not implemented yet" {
				t.Errorf("got error message %q, want %q", body.Error.Message, "endpoint not implemented yet")
			}
		})
	}
}

func TestReadyz(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		ready          httpserver.ReadinessChecker
		wantStatus     int
		wantBodyStatus string
	}{
		{
			name:           "no storage wired is degraded",
			ready:          nil,
			wantStatus:     http.StatusServiceUnavailable,
			wantBodyStatus: "degraded",
		},
		{
			name:           "failing dependency is degraded",
			ready:          &readinessStub{err: errors.New("connection refused")},
			wantStatus:     http.StatusServiceUnavailable,
			wantBodyStatus: "degraded",
		},
		{
			name:           "healthy dependency is ok",
			ready:          &readinessStub{},
			wantStatus:     http.StatusOK,
			wantBodyStatus: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			httpserver.Handler(tt.ready).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("got Content-Type %q, want application/json", ct)
			}

			var got api.HealthStatus
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body %q is not the contract HealthStatus shape: %v", rec.Body.String(), err)
			}
			if string(got.Status) != tt.wantBodyStatus {
				t.Errorf("got status field %q, want %q", got.Status, tt.wantBodyStatus)
			}
		})
	}
}

// TestReadyzBoundsProbe pins that the readiness handler gives the dependency
// check a deadline: a stalled database must turn into a fast 503.
func TestReadyzBoundsProbe(t *testing.T) {
	t.Parallel()

	stub := &readinessStub{}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	httpserver.Handler(stub).ServeHTTP(httptest.NewRecorder(), req)

	if !stub.gotDeadline {
		t.Error("readiness probe context has no deadline")
	}
}
