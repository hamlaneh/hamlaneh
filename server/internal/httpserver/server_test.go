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
			name:            "root serves login page HTML",
			method:          http.MethodGet,
			path:            "/",
			wantStatus:      http.StatusOK,
			wantContentType: "text/html",
			wantBodyContain: "Hamlaneh",
		},
		{
			name:            "stylesheet is served",
			method:          http.MethodGet,
			path:            "/static/style.css",
			wantStatus:      http.StatusOK,
			wantContentType: "text/css",
			wantBodyContain: "body",
		},
		{
			name:       "unknown path is 404",
			method:     http.MethodGet,
			path:       "/no-such-page",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unknown static asset is 404",
			method:     http.MethodGet,
			path:       "/static/no-such-file.css",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "static directory listing is blocked",
			method:     http.MethodGet,
			path:       "/static/",
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

// TestLoginPageCSPCompliance pins the contract that the served HTML carries
// no inline script or style: Caddy sets a strict CSP that would break them.
func TestLoginPageCSPCompliance(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	httpserver.Handler(nil).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script") {
		t.Error("login page contains an inline <script> tag")
	}
	if strings.Contains(body, "<style") {
		t.Error("login page contains an inline <style> tag")
	}
	if strings.Contains(body, "style=") {
		t.Error("login page contains an inline style attribute")
	}
	if !strings.Contains(body, `lang="en"`) {
		t.Error(`login page is missing lang="en"`)
	}
	if !strings.Contains(body, "/static/style.css") {
		t.Error("login page does not link /static/style.css")
	}
	if !strings.Contains(body, "<form") {
		t.Error("login page is missing the sign-in form")
	}
}

func TestNewSetsTimeouts(t *testing.T) {
	t.Parallel()

	srv := httpserver.New(":8080", nil)
	if srv.Addr != ":8080" {
		t.Errorf("got addr %q, want %q", srv.Addr, ":8080")
	}
	if srv.Handler == nil {
		t.Error("handler is nil")
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
