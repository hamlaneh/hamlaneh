package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
)

// The update control (ADR 016). Who may call these is the authz matrix's
// business (internal/authztest); what they do with a state directory is this
// file's.

// updateDir builds a state directory holding the named files and returns its
// path.
func updateDir(t *testing.T, files map[string]string) string {
	t.Helper()
	path := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(path, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return path
}

// listeningWatcher is a watcher stamp fresh enough to switch the control on.
func listeningWatcher() string {
	return `{"schema":1,"installed_at":"2026-01-01T00:00:00Z","last_seen":"` +
		time.Now().UTC().Format(time.RFC3339) + `","channel":"security"}`
}

// updateServer serves one request against a server wired with a version and a
// state directory.
func updateServer(t *testing.T, version, stateDir string, req *http.Request, opts ...httpserver.Option) *httptest.ResponseRecorder {
	t.Helper()
	opts = append([]httpserver.Option{
		httpserver.WithVersion(version),
		httpserver.WithUpdateState(stateDir),
	}, opts...)
	rec := httptest.NewRecorder()
	httpserver.Handler(adminStore(), opts...).ServeHTTP(rec, req)
	return rec
}

func getUpdateStatus(t *testing.T, rec *httptest.ResponseRecorder) api.UpdateStatus {
	t.Helper()
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d (body %s)", rec.Code, rec.Body.String())
	}
	var body api.UpdateStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the contract shape: %v (%s)", err, rec.Body.String())
	}
	return body
}

// TestUpdateStatusWithNothingListening is the shape most installs are in: no
// state directory at all. The endpoint answers, and every field says so.
func TestUpdateStatusWithNothingListening(t *testing.T) {
	t.Parallel()

	rec := updateServer(t, "v1.4.0", "", adminAPI(http.MethodGet, "/api/v1/admin/update", ""))
	status := getUpdateStatus(t, rec)

	if status.SelfUpdateAvailable {
		t.Error("self_update_available is true with no state directory")
	}
	if status.InstalledVersion != "v1.4.0" {
		t.Errorf("installed_version = %q, want the server's own version", status.InstalledVersion)
	}
	if !status.Updatable {
		t.Error("a released version reports updatable false")
	}
	if status.State != api.Unknown {
		t.Errorf("state = %q, want unknown", status.State)
	}
	if status.Channel != api.Security {
		t.Errorf("channel = %q, want the security default", status.Channel)
	}
	if status.AvailableVersion != nil || status.Message != nil {
		t.Errorf("a host that said nothing produced %+v", status)
	}
	if status.LastCheckAt != nil || status.LastRunAt != nil {
		t.Errorf("a host that ran nothing produced timestamps: %+v", status)
	}
}

// TestInstalledVersionComesFromTheBinary: what this instance runs is
// something the server knows about itself. A state directory that could
// answer it would be choosing what the instance claims to be running.
func TestInstalledVersionComesFromTheBinary(t *testing.T) {
	t.Parallel()

	dir := updateDir(t, map[string]string{
		"watcher.json": listeningWatcher(),
		"status.json": `{"schema":1,"state":"succeeded","installed_version":"v9.9.9",
		 "available_version":"v1.4.1","channel":"security"}`,
	})
	status := getUpdateStatus(t, updateServer(t, "v1.4.0", dir,
		adminAPI(http.MethodGet, "/api/v1/admin/update", "")))

	if status.InstalledVersion != "v1.4.0" {
		t.Errorf("installed_version = %q; the state directory named the version", status.InstalledVersion)
	}
}

// TestUpdatableFollowsTheVersionString pins the one control standing between
// a signed old release and this instance: an installed version the
// anti-rollback check cannot parse means the updater refuses the run, and the
// dashboard has to say so rather than offering a button that would fail.
func TestUpdatableFollowsTheVersionString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version string
		want    bool
	}{
		{"v1.4.0", true},
		{"v0.0.1", true},
		{"v10.20.30", true},
		{"v1.4.0-rc.1", true},
		{"v1.4.0+build.5", true},
		// A build from a git checkout, which is what the project's own server
		// runs and why the timer on it has been failing.
		{"dev", false},
		{"", false},
		{"1.4.0", false},
		{"v1.4", false},
		{"v1.4.0.1", false},
		{"latest", false},
		{"v1.4.0 ", false},
	}

	for _, tt := range tests {
		t.Run("version "+tt.version, func(t *testing.T) {
			t.Parallel()
			status := getUpdateStatus(t, updateServer(t, tt.version, "",
				adminAPI(http.MethodGet, "/api/v1/admin/update", "")))
			if status.Updatable != tt.want {
				t.Errorf("updatable = %v for %q, want %v", status.Updatable, tt.version, tt.want)
			}
			// Whatever it was, the screen is told exactly what is installed.
			want := tt.version
			if want == "" {
				want = "dev"
			}
			if status.InstalledVersion != want {
				t.Errorf("installed_version = %q, want %q", status.InstalledVersion, want)
			}
		})
	}
}

// TestUpdateStatusReportsWhatTheHostWrote is the screen's own data path.
func TestUpdateStatusReportsWhatTheHostWrote(t *testing.T) {
	t.Parallel()

	dir := updateDir(t, map[string]string{
		"watcher.json": listeningWatcher(),
		"status.json": `{"schema":1,"id":"run-1","state":"rolled_back","channel":"all",
		 "available_version":"v2.0.0","available_outside_channel":true,"exit_code":1,
		 "started_at":"2026-09-08T11:00:00Z","finished_at":"2026-09-08T11:04:00Z",
		 "checked_at":"2026-09-08T11:00:02Z","message":"v2.0.0 did not come up; v1.4.0 is serving"}`,
	})
	status := getUpdateStatus(t, updateServer(t, "v1.4.0", dir,
		adminAPI(http.MethodGet, "/api/v1/admin/update", "")))

	if !status.SelfUpdateAvailable {
		t.Error("a fresh watcher stamp did not switch the control on")
	}
	if status.State != api.RolledBack {
		t.Errorf("state = %q, want rolled_back", status.State)
	}
	if status.Channel != api.All {
		t.Errorf("channel = %q, want all", status.Channel)
	}
	if status.AvailableVersion == nil || *status.AvailableVersion != "v2.0.0" {
		t.Errorf("available_version = %v", status.AvailableVersion)
	}
	if status.AvailableOutsideChannel == nil || !*status.AvailableOutsideChannel {
		t.Errorf("available_outside_channel = %v, want true", status.AvailableOutsideChannel)
	}
	if status.Message == nil || !strings.Contains(*status.Message, "did not come up") {
		t.Errorf("message = %v", status.Message)
	}
	if status.LastRunAt == nil || status.LastCheckAt == nil {
		t.Fatalf("timestamps missing: %+v", status)
	}
	if want := time.Date(2026, 9, 8, 11, 4, 0, 0, time.UTC); !status.LastRunAt.Equal(want) {
		t.Errorf("last_run_at = %v, want %v", status.LastRunAt, want)
	}
}

// TestUnreadableStateNeverBreaksTheEndpoint is the rule that matters most on
// this surface: the files come from outside the process, and an operator
// opens this screen precisely when something has gone wrong. A 500 here would
// take away the one place that could have told them what.
func TestUnreadableStateNeverBreaksTheEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
	}{
		{"an empty directory", nil},
		{"a status that is not JSON", map[string]string{
			"watcher.json": listeningWatcher(),
			"status.json":  "the update went fine, honest",
		}},
		{"a truncated status", map[string]string{
			"watcher.json": listeningWatcher(),
			"status.json":  `{"schema":1,"state":"succ`,
		}},
		{"a watcher that is not JSON", map[string]string{
			"watcher.json": "listening",
			"status.json":  `{"schema":1,"state":"idle"}`,
		}},
		{"a state nobody defined", map[string]string{
			"watcher.json": listeningWatcher(),
			"status.json":  `{"schema":1,"state":"exploded"}`,
		}},
		{"a channel nobody defined", map[string]string{
			"watcher.json": listeningWatcher(),
			"status.json":  `{"schema":1,"state":"idle","channel":"everything"}`,
		}},
		{"wrong field types throughout", map[string]string{
			"watcher.json": `{"schema":"1","last_seen":42}`,
			"status.json":  `{"schema":1,"state":["idle"],"message":{"a":1}}`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := updateServer(t, "v1.4.0", updateDir(t, tt.files),
				adminAPI(http.MethodGet, "/api/v1/admin/update", ""))
			status := getUpdateStatus(t, rec)
			if !status.State.Valid() {
				t.Errorf("state %q is not one the contract knows", status.State)
			}
			if !status.Channel.Valid() {
				t.Errorf("channel %q is not one the contract knows", status.Channel)
			}
		})
	}
}

// TestRequestUpdateWritesTheRequestAndRecordsIt is the click, end to end: a
// file the host will find, an audit entry naming which of the two was asked
// for, and a body that already says the host has not answered yet.
func TestRequestUpdateWritesTheRequestAndRecordsIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind   string
		action string
	}{
		{"check", "update.check_requested"},
		{"apply", "update.apply_requested"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()

			dir := updateDir(t, map[string]string{
				"watcher.json": listeningWatcher(),
				"status.json":  `{"schema":1,"id":"an-older-run","state":"idle","channel":"security"}`,
			})
			log := &recordingAudit{}
			rec := updateServer(t, "v1.4.0", dir,
				adminAPI(http.MethodPost, "/api/v1/admin/update", `{"kind":"`+tt.kind+`"}`),
				httpserver.WithAudit(log))

			if rec.Code != http.StatusAccepted {
				t.Fatalf("got status %d, want 202 (body %s)", rec.Code, rec.Body.String())
			}
			if status := getUpdateStatus(t, rec); status.State != api.Requested {
				t.Errorf("state = %q, want requested", status.State)
			}

			raw, err := os.ReadFile(filepath.Join(dir, "request.json"))
			if err != nil {
				t.Fatalf("no request was written: %v", err)
			}
			var written map[string]any
			if err = json.Unmarshal(raw, &written); err != nil {
				t.Fatalf("the request is not JSON: %v (%s)", err, raw)
			}
			if written["kind"] != tt.kind {
				t.Errorf("kind = %v, want %q", written["kind"], tt.kind)
			}
			// The security property of the whole design: what reaches the
			// host names no version, no repository and no flag.
			if len(written) != 4 {
				t.Errorf("the request carries %d fields (%s), want exactly four", len(written), raw)
			}
			if !containsString(log.actions(), tt.action) {
				t.Errorf("audit actions = %v, want %s", log.actions(), tt.action)
			}
		})
	}
}

// TestRequestUpdateWithoutAWatcher: a request nothing will read is worse than
// a refusal, because the operator would believe it was picked up.
func TestRequestUpdateWithoutAWatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		dir   bool
	}{
		{"no state directory at all", nil, false},
		{"a directory nothing has stamped", nil, true},
		{"a watcher that stopped refreshing", map[string]string{
			"watcher.json": `{"schema":1,"last_seen":"2026-01-01T00:00:00Z","channel":"security"}`,
		}, true},
		{"a watcher that does not parse", map[string]string{
			"watcher.json": `{"schema":1,"last_seen":`,
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := ""
			if tt.dir {
				path = updateDir(t, tt.files)
			}
			log := &recordingAudit{}
			rec := updateServer(t, "v1.4.0", path,
				adminAPI(http.MethodPost, "/api/v1/admin/update", `{"kind":"apply"}`),
				httpserver.WithAudit(log))

			wantError(t, rec, http.StatusServiceUnavailable, "self_update_unavailable")
			if len(log.actions()) != 0 {
				t.Errorf("a refused request was recorded as %v", log.actions())
			}
			if tt.dir {
				if _, err := os.Stat(filepath.Join(path, "request.json")); err == nil {
					t.Error("a refused request was written to the directory anyway")
				}
			}
		})
	}
}

// TestRequestUpdateWhileOneIsInFlight: the updater takes a host-wide lock, so
// a second run is not something this endpoint could grant even if it wanted
// to — and overwriting the request file would lose the id the dashboard is
// following.
func TestRequestUpdateWhileOneIsInFlight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
	}{
		{"a click the host has not picked up", map[string]string{
			"request.json": `{"schema":1,"id":"the-first-click","kind":"apply"}`,
		}},
		{"a run the host is in the middle of", map[string]string{
			"status.json": `{"schema":1,"id":"the-first-click","state":"running"}`,
		}},
		{"the scheduled timer, which carries no request id", map[string]string{
			"status.json": `{"schema":1,"id":"","state":"running"}`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			files := map[string]string{"watcher.json": listeningWatcher()}
			for name, body := range tt.files {
				files[name] = body
			}
			dir := updateDir(t, files)
			before, _ := os.ReadFile(filepath.Join(dir, "request.json"))

			log := &recordingAudit{}
			rec := updateServer(t, "v1.4.0", dir,
				adminAPI(http.MethodPost, "/api/v1/admin/update", `{"kind":"apply"}`),
				httpserver.WithAudit(log))

			wantError(t, rec, http.StatusConflict, "update_in_progress")
			if len(log.actions()) != 0 {
				t.Errorf("a refused request was recorded as %v", log.actions())
			}
			after, _ := os.ReadFile(filepath.Join(dir, "request.json"))
			if string(after) != string(before) {
				t.Errorf("the refused request replaced the one in flight: %s", after)
			}
		})
	}
}

// TestRequestUpdateRefusesEveryOtherBody. The kind is the only field the
// contract has, and it is the only field the host reads; a body that could
// carry more is the endpoint this design exists not to be (ADR 016 §1).
func TestRequestUpdateRefusesEveryOtherBody(t *testing.T) {
	t.Parallel()

	bodies := []string{
		`{}`,
		`{"kind":""}`,
		`{"kind":"Apply"}`,
		`{"kind":"rollback"}`,
		`{"kind":"apply --force"}`,
		`{"kind":["apply"]}`,
		`{"kind":"apply","version":"v1.0.0","force":true}`,
		`not json`,
		``,
	}

	for _, body := range bodies {
		t.Run("body "+body, func(t *testing.T) {
			t.Parallel()

			dir := updateDir(t, map[string]string{"watcher.json": listeningWatcher()})
			rec := updateServer(t, "v1.4.0", dir,
				adminAPI(http.MethodPost, "/api/v1/admin/update", body))

			// A body naming a version alongside a valid kind is accepted —
			// the extra field is ignored, which is what makes it harmless —
			// so the assertion is about what reached the host, not about the
			// status.
			raw, err := os.ReadFile(filepath.Join(dir, "request.json"))
			if err != nil {
				if rec.Code != http.StatusBadRequest {
					t.Errorf("got status %d, want 400 (body %s)", rec.Code, rec.Body.String())
				}
				return
			}
			if strings.Contains(string(raw), "force") || strings.Contains(string(raw), "v1.0.0") {
				t.Errorf("a field from the request body reached the host: %s", raw)
			}
		})
	}
}

// TestUpdateRequestIsBudgeted holds the contract's 429 to a real limit: this
// is the one admin endpoint whose click leaves the process.
func TestUpdateRequestIsBudgeted(t *testing.T) {
	t.Parallel()

	dir := updateDir(t, map[string]string{"watcher.json": listeningWatcher()})
	handler := httpserver.Handler(adminStore(),
		httpserver.WithVersion("v1.4.0"), httpserver.WithUpdateState(dir))

	// Each accepted request replaces the last, so the state never becomes
	// requested from the server's side alone — the host is what would answer
	// it — and a fresh status file keeps the 409 out of the way.
	statuses := make([]int, 0, 6)
	for i := 0; i < 6; i++ {
		if err := os.WriteFile(filepath.Join(dir, "status.json"),
			[]byte(`{"schema":1,"id":"","state":"idle"}`), 0o600); err != nil {
			t.Fatalf("write the status: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, "request.json")); err != nil && !os.IsNotExist(err) {
			t.Fatalf("clear the request: %v", err)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, adminAPI(http.MethodPost, "/api/v1/admin/update", `{"kind":"check"}`))
		statuses = append(statuses, rec.Code)
	}

	for i, code := range statuses[:5] {
		if code != http.StatusAccepted {
			t.Errorf("request %d answered %d, want 202", i+1, code)
		}
	}
	if statuses[5] != http.StatusTooManyRequests {
		t.Errorf("the sixth request answered %d, want 429", statuses[5])
	}
}
