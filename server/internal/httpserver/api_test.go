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

func TestReadyz(t *testing.T) {
	t.Parallel()

	deadlineSeen := false
	tests := []struct {
		name           string
		store          httpserver.Store
		wantStatus     int
		wantBodyStatus string
	}{
		{
			name:           "no storage wired is degraded",
			store:          nil,
			wantStatus:     http.StatusServiceUnavailable,
			wantBodyStatus: "degraded",
		},
		{
			name: "failing dependency is degraded",
			store: &fakeStore{ready: func(context.Context) error {
				return errors.New("connection refused")
			}},
			wantStatus:     http.StatusServiceUnavailable,
			wantBodyStatus: "degraded",
		},
		{
			name: "healthy dependency is ok",
			store: &fakeStore{ready: func(ctx context.Context) error {
				_, deadlineSeen = ctx.Deadline()
				return nil
			}},
			wantStatus:     http.StatusOK,
			wantBodyStatus: "ok",
		},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := do(t, tt.store, req)

		if rec.Code != tt.wantStatus {
			t.Errorf("%s: got status %d, want %d", tt.name, rec.Code, tt.wantStatus)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: got Content-Type %q, want application/json", tt.name, ct)
		}

		var got api.HealthStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: body %q is not the contract HealthStatus shape: %v", tt.name, rec.Body.String(), err)
		}
		if string(got.Status) != tt.wantBodyStatus {
			t.Errorf("%s: got status field %q, want %q", tt.name, got.Status, tt.wantBodyStatus)
		}
	}

	// Pinned behavior: the readiness handler bounds the dependency probe
	// with a deadline so a stalled database turns into a fast 503.
	if !deadlineSeen {
		t.Error("readiness probe context has no deadline")
	}
}

// TestMalformedParamsAnswerJSON pins the replacement of oapi-codegen's
// plain-text 400: malformed query parameters must answer with the
// contract's JSON Error shape.
func TestMalformedParamsAnswerJSON(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users?limit=not-a-number", nil)
	rec := do(t, &fakeStore{}, req)

	wantError(t, rec, http.StatusBadRequest, "invalid_request")
}
