package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
)

// TestRunHealthcheckSubcommand exercises the healthcheck subcommand end to
// end against a test server by overriding the probe URL. Subtests mutate
// package state, so they must not run in parallel.
func TestRunHealthcheckSubcommand(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			t.Errorf("write test response: %v", err)
		}
	}))
	defer healthy.Close()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()

	tests := []struct {
		name    string
		args    []string
		url     string
		wantErr bool
	}{
		{name: "healthy server exits zero", args: []string{"healthcheck"}, url: healthy.URL, wantErr: false},
		{name: "unhealthy server exits non-zero", args: []string{"healthcheck"}, url: unhealthy.URL, wantErr: true},
		{name: "extra arguments are rejected", args: []string{"healthcheck", "extra"}, url: healthy.URL, wantErr: true},
		{name: "unknown command is rejected", args: []string{"bogus"}, url: healthy.URL, wantErr: true},
	}

	originalURL := healthcheckURL
	t.Cleanup(func() { healthcheckURL = originalURL })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			healthcheckURL = tt.url
			err := run(tt.args)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("run(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestServeShutsDownOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, httpserver.New("127.0.0.1:0", nil))
	}()

	// Give the server a moment to start, then request shutdown.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve() after context cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return within 5s of context cancel")
	}
}

func TestServeInvalidAddress(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := serve(ctx, httpserver.New("127.0.0.1:99999", nil)); err == nil {
		t.Error("serve() with an invalid port returned nil, want error")
	}
}
