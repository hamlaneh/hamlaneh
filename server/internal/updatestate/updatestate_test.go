package updatestate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/updatestate"
)

// now is the moment every test reads the directory at. Fixed, because a
// staleness window measured against the wall clock is a test that fails at
// midnight.
var now = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// stamp formats a moment the way the host's updater writes one.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// dirWith builds a state directory holding the named files, and returns it
// alongside its path so a test can inspect what was written.
func dirWith(t *testing.T, files map[string]string) (updatestate.Dir, string) {
	t.Helper()
	path := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(path, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return updatestate.New(path), path
}

// watcherStamp is a watcher file last seen at the given moment.
func watcherStamp(seen time.Time, channel string) string {
	return `{"schema":1,"installed_at":"2026-09-01T09:00:00Z","last_seen":"` +
		stamp(seen) + `","channel":"` + channel + `"}`
}

func TestReadWithoutADirectory(t *testing.T) {
	t.Parallel()

	// The zero Dir is a deployment that never installed the host side. It is
	// a supported install, so the answer is a usable snapshot rather than an
	// error, and every field says the honest thing.
	var none updatestate.Dir
	if none.Configured() {
		t.Error("the zero Dir reports itself configured")
	}
	snap := none.Read(now)
	if snap.Watching {
		t.Error("nothing is listening, but the snapshot says something is")
	}
	if snap.State != updatestate.StateUnknown {
		t.Errorf("state = %q, want unknown", snap.State)
	}
	if snap.Channel != updatestate.ChannelSecurity {
		t.Errorf("channel = %q, want the security default", snap.Channel)
	}
}

func TestReadOfAnEmptyDirectory(t *testing.T) {
	t.Parallel()

	dir, _ := dirWith(t, nil)
	snap := dir.Read(now)
	if snap.Watching {
		t.Error("an empty directory claims something is listening")
	}
	if snap.State != updatestate.StateUnknown {
		t.Errorf("state = %q, want unknown", snap.State)
	}
	if !snap.LastCheckAt.IsZero() || !snap.LastRunAt.IsZero() {
		t.Errorf("timestamps = %v / %v, want zero", snap.LastCheckAt, snap.LastRunAt)
	}
	if snap.Message != "" {
		t.Errorf("message = %q, want empty", snap.Message)
	}
}

// TestWatcherFreshness is the control's on/off switch, and the reason it is a
// window rather than a flag: a host whose units were removed stops refreshing
// the stamp and the claim expires on its own.
func TestWatcherFreshness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		watcher string
		want    bool
	}{
		{"stamped moments ago", watcherStamp(now.Add(-time.Minute), "security"), true},
		{"stamped six hours ago, one missed run", watcherStamp(now.Add(-6*time.Hour), "security"), true},
		{"stamped just inside the window", watcherStamp(now.Add(-47*time.Hour), "security"), true},
		{"stamped exactly at the window", watcherStamp(now.Add(-48*time.Hour), "security"), false},
		{"stamped a week ago", watcherStamp(now.Add(-7*24*time.Hour), "security"), false},
		// A host whose clock runs ahead is still a host that stamped the file.
		{"stamped slightly in the future", watcherStamp(now.Add(time.Minute), "security"), true},
		{"no last_seen at all", `{"schema":1,"channel":"security"}`, false},
		{"an unreadable last_seen", `{"schema":1,"last_seen":"yesterday"}`, false},
		{"a schema this binary does not know", `{"schema":2,"last_seen":"` + stamp(now) + `"}`, false},
		{"not JSON", "listening!", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, _ := dirWith(t, map[string]string{"watcher.json": tt.watcher})
			if got := dir.Read(now).Watching; got != tt.want {
				t.Errorf("Watching = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReadStatus maps a whole status file, which is what the dashboard draws.
func TestReadStatus(t *testing.T) {
	t.Parallel()

	dir, _ := dirWith(t, map[string]string{
		"watcher.json": watcherStamp(now.Add(-time.Hour), "security"),
		"status.json": `{"schema":1,"id":"","state":"succeeded","available_version":"v1.4.1",
		 "available_outside_channel":false,"channel":"security","exit_code":0,
		 "started_at":"2026-09-08T11:00:00Z","finished_at":"2026-09-08T11:02:00Z",
		 "checked_at":"2026-09-08T11:00:05Z","message":"updated to v1.4.1"}`,
	})

	snap := dir.Read(now)
	if !snap.Watching {
		t.Error("a fresh watcher stamp did not switch the control on")
	}
	if snap.State != updatestate.StateSucceeded {
		t.Errorf("state = %q, want succeeded", snap.State)
	}
	if snap.AvailableVersion != "v1.4.1" {
		t.Errorf("available_version = %q", snap.AvailableVersion)
	}
	if snap.AvailableOutsideChannel {
		t.Error("available_outside_channel is true, but the file says false")
	}
	if want := time.Date(2026, 9, 8, 11, 0, 5, 0, time.UTC); !snap.LastCheckAt.Equal(want) {
		t.Errorf("last check = %v, want %v", snap.LastCheckAt, want)
	}
	// The last run is when it finished, not when it started.
	if want := time.Date(2026, 9, 8, 11, 2, 0, 0, time.UTC); !snap.LastRunAt.Equal(want) {
		t.Errorf("last run = %v, want %v", snap.LastRunAt, want)
	}
	if snap.Message != "updated to v1.4.1" {
		t.Errorf("message = %q", snap.Message)
	}
}

// TestRunningRunReportsWhenItStarted: a run in flight has no finish, and
// leaving last_run_at empty for the whole length of the run is the one moment
// an operator is definitely watching the screen.
func TestRunningRunReportsWhenItStarted(t *testing.T) {
	t.Parallel()

	dir, _ := dirWith(t, map[string]string{
		"status.json": `{"schema":1,"state":"running","started_at":"2026-09-08T11:59:00Z",
		 "finished_at":"","checked_at":""}`,
	})
	snap := dir.Read(now)
	if snap.State != updatestate.StateRunning {
		t.Errorf("state = %q, want running", snap.State)
	}
	if want := time.Date(2026, 9, 8, 11, 59, 0, 0, time.UTC); !snap.LastRunAt.Equal(want) {
		t.Errorf("last run = %v, want the start %v", snap.LastRunAt, want)
	}
	if !snap.LastCheckAt.IsZero() {
		t.Errorf("last check = %v, want zero", snap.LastCheckAt)
	}
}

// TestHostStates pins every word the host may write, and what everything else
// becomes. The two this package owns are in the table too: a status file
// claiming them is as meaningless as one claiming a word nobody defined.
func TestHostStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want updatestate.State
	}{
		{"idle", updatestate.StateIdle},
		{"running", updatestate.StateRunning},
		{"succeeded", updatestate.StateSucceeded},
		{"failed", updatestate.StateFailed},
		{"rolled_back", updatestate.StateRolledBack},
		{"refused", updatestate.StateRefused},
		{"requested", updatestate.StateUnknown},
		{"unknown", updatestate.StateUnknown},
		{"", updatestate.StateUnknown},
		{"SUCCEEDED", updatestate.StateUnknown},
		{"probably fine", updatestate.StateUnknown},
	}

	for _, tt := range tests {
		t.Run("state "+tt.raw, func(t *testing.T) {
			t.Parallel()
			dir, _ := dirWith(t, map[string]string{
				"status.json": `{"schema":1,"state":"` + tt.raw + `"}`,
			})
			if got := dir.Read(now).State; got != tt.want {
				t.Errorf("state %q read as %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestChannelStaysInsideItsEnum: the field feeds a contract enum a client
// switches on, and it is written by another process.
func TestChannelStaysInsideItsEnum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		want  updatestate.Channel
	}{
		{
			name:  "nothing said",
			files: nil,
			want:  updatestate.ChannelSecurity,
		},
		{
			name:  "the watcher says all",
			files: map[string]string{"watcher.json": watcherStamp(now, "all")},
			want:  updatestate.ChannelAll,
		},
		{
			name: "the last run says all",
			files: map[string]string{
				"watcher.json": watcherStamp(now, "security"),
				"status.json":  `{"schema":1,"state":"idle","channel":"all"}`,
			},
			want: updatestate.ChannelAll,
		},
		{
			name:  "a channel nobody defined",
			files: map[string]string{"watcher.json": watcherStamp(now, "everything, fast")},
			want:  updatestate.ChannelSecurity,
		},
		{
			name: "a status with no channel does not erase the watcher's",
			files: map[string]string{
				"watcher.json": watcherStamp(now, "all"),
				"status.json":  `{"schema":1,"state":"idle"}`,
			},
			want: updatestate.ChannelAll,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, _ := dirWith(t, tt.files)
			if got := dir.Read(now).Channel; got != tt.want {
				t.Errorf("channel = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRequestedUntilTheHostAnswers is the state resolution the 409 is built
// on: a request file the status file does not name is a click nothing has
// picked up.
func TestRequestedUntilTheHostAnswers(t *testing.T) {
	t.Parallel()

	const id = "8f14e45f-ceea-467a-9e77-e8b0c3e34567"
	tests := []struct {
		name  string
		files map[string]string
		want  updatestate.State
	}{
		{
			name:  "a request and no status at all",
			files: map[string]string{"request.json": `{"schema":1,"id":"` + id + `","kind":"apply"}`},
			want:  updatestate.StateRequested,
		},
		{
			name: "a request the last status does not name",
			files: map[string]string{
				"request.json": `{"schema":1,"id":"` + id + `","kind":"apply"}`,
				"status.json":  `{"schema":1,"id":"an-older-run","state":"succeeded"}`,
			},
			want: updatestate.StateRequested,
		},
		{
			name: "a request the status has answered",
			files: map[string]string{
				"request.json": `{"schema":1,"id":"` + id + `","kind":"apply"}`,
				"status.json":  `{"schema":1,"id":"` + id + `","state":"succeeded"}`,
			},
			want: updatestate.StateSucceeded,
		},
		{
			name: "no request left, so the status stands alone",
			files: map[string]string{
				"status.json": `{"schema":1,"id":"` + id + `","state":"running"}`,
			},
			want: updatestate.StateRunning,
		},
		{
			// The file being there is the evidence somebody clicked; an id
			// nobody can read does not make the click go away.
			name:  "a request with no readable id",
			files: map[string]string{"request.json": `{"schema":1,"kind":"apply"}`},
			want:  updatestate.StateRequested,
		},
		{
			// Unreadable is absent, and an absent request is not a pending one.
			name:  "a request that is not JSON",
			files: map[string]string{"request.json": `please update`},
			want:  updatestate.StateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, _ := dirWith(t, tt.files)
			if got := dir.Read(now).State; got != tt.want {
				t.Errorf("state = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUnreadableFilesAreAbsentNotFailures is the whole tolerance contract in
// one table. Every one of these arrives from outside the process, and none of
// them may stop the endpoint answering.
func TestUnreadableFilesAreAbsentNotFailures(t *testing.T) {
	t.Parallel()

	huge := `{"schema":1,"state":"succeeded","message":"` + strings.Repeat("x", 128<<10) + `"}`
	tests := []struct {
		name   string
		status string
	}{
		{"empty", ""},
		{"not JSON", "\x00\x01\x02 not json"},
		{"truncated", `{"schema":1,"state":"succ`},
		{"a JSON array", `["schema", 1]`},
		{"a JSON string", `"succeeded"`},
		{"wrong field types", `{"schema":1,"state":42,"available_outside_channel":"yes"}`},
		{"no schema", `{"state":"succeeded"}`},
		{"a schema from the future", `{"schema":99,"state":"succeeded"}`},
		{"larger than the read cap", huge},
		{"trailing junk", `{"schema":1,"state":"succeeded"} and then some`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, _ := dirWith(t, map[string]string{
				"watcher.json": watcherStamp(now, "security"),
				"status.json":  tt.status,
			})
			snap := dir.Read(now)
			// The watcher beside it is untouched: one unreadable file does not
			// take the rest of the directory down with it.
			if !snap.Watching {
				t.Error("an unreadable status file switched the control off")
			}
			if snap.State != updatestate.StateUnknown {
				t.Errorf("state = %q, want unknown", snap.State)
			}
			if snap.Message != "" {
				t.Errorf("message = %q, want empty", snap.Message)
			}
		})
	}
}

// TestMessageIsBoundedAndDefanged: the line is the host's, it is displayed
// rather than interpreted, and it is not this server's job to make it
// interesting.
func TestMessageIsBoundedAndDefanged(t *testing.T) {
	t.Parallel()

	t.Run("a long message is cut", func(t *testing.T) {
		t.Parallel()
		// A three-byte character, so the cap does not divide evenly and the cut
		// lands inside one — which is the case the trim exists for.
		dir, _ := dirWith(t, map[string]string{
			"status.json": `{"schema":1,"state":"failed","message":"` + strings.Repeat("あ", 4000) + `"}`,
		})
		msg := dir.Read(now).Message
		if len(msg) == 0 || len(msg) > 4<<10 {
			t.Errorf("message is %d bytes, want between 1 and 4096", len(msg))
		}
		// Cutting on a byte count must not leave half a character behind.
		if strings.ContainsRune(msg, '�') {
			t.Error("the cut split a character")
		}
	})

	t.Run("control characters are dropped, text is not", func(t *testing.T) {
		t.Parallel()
		dir, _ := dirWith(t, map[string]string{
			"status.json": `{"schema":1,"state":"failed",` +
				`"message":"pulled \u0007ghcr.io/x\u001b[31m\nline two\tand a tab — نسخه"}`,
		})
		msg := dir.Read(now).Message
		for _, banned := range []string{"\a", "\x1b", "\x7f"} {
			if strings.Contains(msg, banned) {
				t.Errorf("message kept %q: %q", banned, msg)
			}
		}
		for _, kept := range []string{"pulled ", "ghcr.io/x", "[31m", "\nline two", "\tand a tab", "نسخه"} {
			if !strings.Contains(msg, kept) {
				t.Errorf("message lost %q: %q", kept, msg)
			}
		}
	})
}

// TestRequestWritesTheContractAndNothingElse is the security shape of the
// write: a schema, an id, one of two literals, and a time. No version, no
// repository, no flag (ADR 016 §1).
func TestRequestWritesTheContractAndNothingElse(t *testing.T) {
	t.Parallel()

	for _, kind := range []updatestate.Kind{updatestate.KindCheck, updatestate.KindApply} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			dir, path := dirWith(t, nil)
			if err := dir.Request(kind, now); err != nil {
				t.Fatalf("Request(%q): %v", kind, err)
			}

			raw, err := os.ReadFile(filepath.Join(path, "request.json"))
			if err != nil {
				t.Fatalf("read back the request: %v", err)
			}
			var written map[string]any
			if err = json.Unmarshal(raw, &written); err != nil {
				t.Fatalf("the request is not JSON: %v (%s)", err, raw)
			}

			wantFields := map[string]any{
				"schema":       float64(1),
				"kind":         string(kind),
				"requested_at": "2026-09-08T12:00:00Z",
			}
			for field, want := range wantFields {
				if got := written[field]; got != want {
					t.Errorf("%s = %v, want %v", field, got, want)
				}
			}
			id, _ := written["id"].(string)
			if _, err = uuid.Parse(id); err != nil {
				t.Errorf("id %q is not a uuid: %v", id, err)
			}
			// Nothing else. A field that is not in the contract is a field the
			// host might one day read.
			if len(written) != 4 {
				t.Errorf("the request carries %d fields (%s), want exactly four", len(written), raw)
			}

			// And the directory holds the request alone: no temporary file
			// survives beside it for the host's watcher to trip over.
			entries, err := os.ReadDir(path)
			if err != nil {
				t.Fatalf("read the directory: %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != "request.json" {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("directory holds %v, want request.json alone", names)
			}
		})
	}
}

// TestRequestRefusesEveryOtherKind: the switch inside Request is what makes
// "only two literals are ever written" true of the code rather than of its
// callers.
func TestRequestRefusesEveryOtherKind(t *testing.T) {
	t.Parallel()

	kinds := []updatestate.Kind{
		"",
		"Apply",
		"apply ",
		"check --force",
		"apply; rm -rf /",
		"rollback",
		`apply","force":true,"x":"`,
	}

	for _, kind := range kinds {
		t.Run("kind "+string(kind), func(t *testing.T) {
			t.Parallel()
			dir, path := dirWith(t, nil)
			if err := dir.Request(kind, now); err == nil {
				t.Fatalf("Request(%q) was accepted", kind)
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				t.Fatalf("read the directory: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("a refused request left %d files behind", len(entries))
			}
		})
	}
}

func TestRequestWithoutADirectory(t *testing.T) {
	t.Parallel()

	var none updatestate.Dir
	if err := none.Request(updatestate.KindApply, now); err == nil {
		t.Fatal("a request against no directory was accepted")
	} else if !strings.Contains(err.Error(), "no state directory") {
		t.Errorf("error = %v, want the not-configured one", err)
	}
}

// TestRequestReplacesTheOneBeforeIt: a second click is a repeat, not a queue.
func TestRequestReplacesTheOneBeforeIt(t *testing.T) {
	t.Parallel()

	dir, path := dirWith(t, nil)
	if err := dir.Request(updatestate.KindCheck, now); err != nil {
		t.Fatalf("first request: %v", err)
	}
	first := readRequestID(t, path)

	if err := dir.Request(updatestate.KindApply, now.Add(time.Minute)); err != nil {
		t.Fatalf("second request: %v", err)
	}
	second := readRequestID(t, path)

	if first == second {
		t.Error("the second request reused the first one's id")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read the directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("two requests left %d files behind, want one", len(entries))
	}
}

// TestRequestIsVisibleAsRequested closes the loop the endpoint depends on:
// what Request writes is what Read reports back, with no host in between.
func TestRequestIsVisibleAsRequested(t *testing.T) {
	t.Parallel()

	dir, _ := dirWith(t, map[string]string{
		"watcher.json": watcherStamp(now, "security"),
		"status.json":  `{"schema":1,"id":"an-older-run","state":"idle"}`,
	})
	if got := dir.Read(now).State; got != updatestate.StateIdle {
		t.Fatalf("state before the request = %q, want idle", got)
	}
	if err := dir.Request(updatestate.KindApply, now); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if got := dir.Read(now).State; got != updatestate.StateRequested {
		t.Errorf("state after the request = %q, want requested", got)
	}
}

func readRequestID(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(path, "request.json"))
	if err != nil {
		t.Fatalf("read the request: %v", err)
	}
	var req struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("the request is not JSON: %v", err)
	}
	return req.ID
}

// FuzzReadStatus is the policy's fuzz target for input-handling code: the
// status file is bytes another process wrote, and Read must answer with a
// state the contract knows however strange they are.
func FuzzReadStatus(f *testing.F) {
	f.Add(`{"schema":1,"state":"succeeded","message":"done"}`)
	f.Add(`{"schema":1,"state":"running","started_at":"2026-09-08T11:59:00Z"}`)
	f.Add(`{"schema":1,"available_version":null,"available_outside_channel":true}`)
	f.Add(`{"schema":1`)
	f.Add(``)
	f.Add(`{"schema":1,"message":"\u0000\u001b[2J"}`)

	known := map[updatestate.State]bool{
		updatestate.StateIdle: true, updatestate.StateRequested: true,
		updatestate.StateRunning: true, updatestate.StateSucceeded: true,
		updatestate.StateFailed: true, updatestate.StateRolledBack: true,
		updatestate.StateRefused: true, updatestate.StateUnknown: true,
	}

	f.Fuzz(func(t *testing.T, status string) {
		path := t.TempDir()
		if err := os.WriteFile(filepath.Join(path, "status.json"), []byte(status), 0o600); err != nil {
			t.Fatalf("write the status: %v", err)
		}
		snap := updatestate.New(path).Read(now)
		if !known[snap.State] {
			t.Errorf("state %q is not one the contract knows", snap.State)
		}
		if snap.Channel != updatestate.ChannelSecurity && snap.Channel != updatestate.ChannelAll {
			t.Errorf("channel %q is not one the contract knows", snap.Channel)
		}
		if len(snap.Message) > 4<<10 {
			t.Errorf("message is %d bytes, past the cap", len(snap.Message))
		}
	})
}
