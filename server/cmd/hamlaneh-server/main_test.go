package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/calls"
	"github.com/hamlaneh/hamlaneh/server/internal/calls/callstest"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
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

// TestVersionFlag is what the updater reads to know what is installed, so the
// output is one line and nothing else: the version, exactly as the release
// tag spells it.
func TestVersionFlag(t *testing.T) {
	var out bytes.Buffer
	original := stdout
	stdout = &out
	t.Cleanup(func() { stdout = original })

	originalVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = originalVersion })

	if err := run([]string{"--version"}); err != nil {
		t.Fatalf("run(--version) = %v, want nil", err)
	}
	if got := out.String(); got != "v1.2.3\n" {
		t.Errorf("run(--version) printed %q, want %q", got, "v1.2.3\n")
	}
}

// A taken port is the most likely first-run failure on a household machine,
// because Docker Desktop usually already holds :8080. The error has to carry
// the way out, so this asserts the remedy reaches the operator rather than
// only that starting failed.
func TestServeOnATakenPortSaysHowToMoveIt(t *testing.T) {
	t.Parallel()

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer func() {
		if cerr := held.Close(); cerr != nil {
			t.Errorf("closing the held listener: %v", cerr)
		}
	}()

	remedy := (mode{home: true}).addrInUseRemedy()
	err = serve(context.Background(), remedy, httpserver.New(held.Addr().String(), nil))
	if err == nil {
		t.Fatal("serve on a port already held returned no error")
	}
	if !strings.Contains(err.Error(), envHomeAddr) {
		t.Errorf("error %q does not name %s, so nobody is told how to move the port", err, envHomeAddr)
	}
}

func TestServeShutsDownOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, "", httpserver.New("127.0.0.1:0", nil))
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

	if err := serve(ctx, "", httpserver.New("127.0.0.1:99999", nil)); err == nil {
		t.Error("serve() with an invalid port returned nil, want error")
	}
}

// TestDeactivationEjectsFromEveryCall is ADR 005's second removal hook, and
// the reason it is a decorator here rather than a call inside each handler:
// deactivation happens on two paths — the admin dashboard's patch and a
// directory's SCIM `active: false` — and both go through this one store
// method. A hook only one of them remembered would be an offboarding that
// half works.
//
// It runs against a real store because that is what the wrapper wraps, and
// against a fake media server because ejection is an RPC. The two facts it
// pins are the whole hook: a committed deactivation ejects, and a
// reactivation does not.
func TestDeactivationEjectsFromEveryCall(t *testing.T) {
	store, _ := testdb.New(t)
	ctx := context.Background()

	// A second admin so the last-admin rule never refuses the change under
	// test — the rule is about a set, and this test is not about it.
	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: "keeper", PasswordHash: "argon2id$fixture", Locale: "en", IsAdmin: true,
	}); err != nil {
		t.Fatalf("create the keeper admin: %v", err)
	}
	leaver, err := store.CreateUser(ctx, storage.NewUser{
		Username: "leaver", PasswordHash: "argon2id$fixture", Locale: "en",
	})
	if err != nil {
		t.Fatalf("create the account to offboard: %v", err)
	}

	lk := callstest.New(t)
	room := calls.RoomFor(uuid.New())
	lk.Rooms = []string{room}
	hooked := offboarding{store, calls.New("k", "a secret long enough to be one", lk.URL, nil)}

	inactive := false
	if _, err := hooked.UpdateUserAdmin(ctx, leaver.ID, storage.AdminUserUpdate{IsActive: &inactive}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	removed := lk.AwaitRemoval(t)
	if removed.GetIdentity() != leaver.ID.String() || removed.GetRoom() != room {
		t.Errorf("ejected %s from %s, want %s from %s",
			removed.GetIdentity(), removed.GetRoom(), leaver.ID, room)
	}

	// Reactivation is not an offboarding and must eject nobody.
	active := true
	if _, err := hooked.UpdateUserAdmin(ctx, leaver.ID, storage.AdminUserUpdate{IsActive: &active}); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	lk.NoRemoval(t)
}
