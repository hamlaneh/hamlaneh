package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
)

func TestHandlerRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		method          string
		path            string
		wantStatus      int
		wantContentType string // prefix match; empty means don't check
		wantBodyContain string // substring match; empty means don't check
	}{
		{
			name:            "healthz returns ok JSON",
			method:          http.MethodGet,
			path:            "/healthz",
			wantStatus:      http.StatusOK,
			wantContentType: "application/json",
			wantBodyContain: `"status"`,
		},
		{
			name:            "root serves the web application document",
			method:          http.MethodGet,
			path:            "/",
			wantStatus:      http.StatusOK,
			wantContentType: "text/html",
			wantBodyContain: "Hamlaneh",
		},
		{
			name:       "unknown path is 404",
			method:     http.MethodGet,
			path:       "/no-such-page",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unknown asset is 404",
			method:     http.MethodGet,
			path:       "/assets/no-such-file.css",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "asset directory listing is blocked",
			method:     http.MethodGet,
			path:       "/assets/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "wrong method on healthz is 405",
			method:     http.MethodPost,
			path:       "/healthz",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "wrong method on root is 405",
			method:     http.MethodDelete,
			path:       "/",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:            "readyz without storage is degraded",
			method:          http.MethodGet,
			path:            "/readyz",
			wantStatus:      http.StatusServiceUnavailable,
			wantContentType: "application/json",
			wantBodyContain: `"degraded"`,
		},
		{
			name:       "wrong method on readyz is 405",
			method:     http.MethodPost,
			path:       "/readyz",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	handler := httpserver.Handler(nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("%s %s: got status %d, want %d", tt.method, tt.path, rec.Code, tt.wantStatus)
			}
			if tt.wantContentType != "" {
				ct := rec.Header().Get("Content-Type")
				if !strings.HasPrefix(ct, tt.wantContentType) {
					t.Errorf("%s %s: got Content-Type %q, want prefix %q", tt.method, tt.path, ct, tt.wantContentType)
				}
			}
			if tt.wantBodyContain != "" && !strings.Contains(rec.Body.String(), tt.wantBodyContain) {
				t.Errorf("%s %s: body does not contain %q", tt.method, tt.path, tt.wantBodyContain)
			}
		})
	}
}

func TestHealthzBody(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	httpserver.Handler(nil).ServeHTTP(rec, req)

	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("healthz body is not valid JSON: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf(`healthz body: got %q, want {"status":"ok"}`, rec.Body.String())
	}
}

// TestServedDocumentCSPCompliance pins that the HTML this binary serves
// carries no inline script or style, which the CSP in securityheaders.go
// would block outright.
//
// In a plain checkout the embedded build is the placeholder (the Go CI job
// does not run `npm run build`), so this covers the placeholder; the same
// property of the REAL bundle is asserted over HTTP by
// deploy/verify-defaults.sh, which runs against a booted stack.
func TestServedDocumentCSPCompliance(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	httpserver.Handler(nil).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script") {
		t.Error("served document contains an inline <script> tag")
	}
	if strings.Contains(body, "<style") {
		t.Error("served document contains an inline <style> tag")
	}
	if strings.Contains(body, "style=") {
		t.Error("served document contains an inline style attribute")
	}
	if !strings.Contains(body, `lang="en"`) {
		t.Error(`served document is missing lang="en"`)
	}
}

func TestNewSetsTimeouts(t *testing.T) {
	t.Parallel()

	srv, admin := httpserver.New(":8080", nil)
	if srv.Addr != ":8080" {
		t.Errorf("got addr %q, want %q", srv.Addr, ":8080")
	}
	if srv.Handler == nil {
		t.Error("handler is nil")
	}
	// No WithAdminListener, so there is no second listener to shut down and
	// nothing moved off the first (ADR 015).
	if admin != nil {
		t.Errorf("New built an admin listener on %q without being asked for one", admin.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout is not set")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout is not set")
	}
	if srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout is not set")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout is not set")
	}
}
