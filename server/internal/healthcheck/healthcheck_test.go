package healthcheck_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/healthcheck"
)

func TestCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "healthy server", status: http.StatusOK, wantErr: false},
		{name: "internal server error", status: http.StatusInternalServerError, wantErr: true},
		{name: "service unavailable", status: http.StatusServiceUnavailable, wantErr: true},
		{name: "not found", status: http.StatusNotFound, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer ts.Close()

			err := healthcheck.Check(context.Background(), ts.URL)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("Check() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckUnreachableServer(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := ts.URL
	ts.Close() // nothing listens there anymore

	if err := healthcheck.Check(context.Background(), url); err == nil {
		t.Error("Check() returned nil for an unreachable server, want error")
	}
}

func TestCheckInvalidURL(t *testing.T) {
	t.Parallel()

	if err := healthcheck.Check(context.Background(), "http://\x00invalid"); err == nil {
		t.Error("Check() returned nil for an invalid URL, want error")
	}
}
