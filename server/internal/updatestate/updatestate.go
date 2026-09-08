// Package updatestate is this server's half of the handoff ADR 016 puts
// between the process and the host that updates it.
//
// The server never updates anything and never runs a command. It writes one
// parameterless request file into a directory the host can also see, and it
// reads two files the host writes back. That asymmetry is the whole design:
// the authority to replace a running image stays on the host, and a click in
// the dashboard crosses the line as a signal rather than as a command.
//
// Everything read here was written by another process, so every read is
// tolerant. A file that is missing, truncated, oversized, malformed, or
// written to a schema this binary does not know is reported as absent rather
// than as a failure — the endpoint above exists to answer an operator who is
// asking precisely because something has gone wrong, and "the server broke"
// is the least useful answer available at that moment.
package updatestate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// EnvStateDir names the directory both sides of the handoff use. Unset or
// empty is a deployment without the feature — a host with no systemd, an
// install whose watcher was never enabled, every stack that predates the
// decision — and the server then reports self_update_available false and
// refuses a request rather than writing one nobody will ever read.
const EnvStateDir = "HAMLANEH_UPDATE_STATE_DIR"

// The three files of the directory. Only one of them is ever written here.
const (
	watcherFile = "watcher.json"
	requestFile = "request.json"
	statusFile  = "status.json"
)

// stateSchema is the shape of those files that this binary understands. Every
// one of them carries it, and a file that says anything else is treated as
// absent: reading fields whose meaning may have moved is how a dashboard says
// something confident and wrong.
const stateSchema = 1

// watcherFreshFor is how long a watcher stamp keeps the control switched on.
//
// The host's timer refreshes the stamp four times a day, so this is eight
// missed runs: comfortably past a reboot, a long maintenance window or a
// laptop that was closed for a weekend, and short enough that a host whose
// units were removed stops claiming to listen. A claim that expires on its
// own beats a flag somebody has to remember to clear — which is the failure
// this window exists to avoid, because a button that silently does nothing
// leaves an operator believing they are patched.
const watcherFreshFor = 48 * time.Hour

// maxStateFileBytes caps a read of a file this process did not write. The
// real ones are a few hundred bytes; anything past this is not a state file,
// and reading it into memory to discover that would be the whole point of
// having written it.
const maxStateFileBytes = 64 << 10

// maxMessageBytes caps the updater's own last line. It is displayed and never
// interpreted, and it arrives from outside this process — a few KB is far
// more than a log line and far less than a payload.
const maxMessageBytes = 4 << 10

// ErrNotConfigured is a request made against a deployment that has no state
// directory. Nothing is broken when it happens: there is simply nothing on
// this host to ask, which is what the endpoint answers 503 for.
var ErrNotConfigured = errors.New("updatestate: no state directory is configured")

// Kind is what a request asks the host to do, and the type exists so that the
// set stays exactly two literals.
//
// ADR 016 §1 is the reason. A request that could name a version could name an
// older one, and the flag that applies an older one is the anti-rollback
// switch — so an endpoint that could pass it would turn a stolen admin
// session into a downgrade to any signed release with a known vulnerability.
// Nothing but the two constants below is ever written to the request file.
type Kind string

const (
	// KindCheck asks the host what a run would do, and changes nothing.
	KindCheck Kind = "check"
	// KindApply asks the host to run the update its timer would have run.
	KindApply Kind = "apply"
)

// State is what the update is doing, in the terms the dashboard reports.
//
// Six of the eight are the host's own words from its status file.
// StateRequested and StateUnknown are this package's and the host cannot
// write either: one is this server's record of a click nothing has answered
// yet, and the other is the absence of an answer.
type State string

const (
	// StateIdle is a host that ran and found nothing to do.
	StateIdle State = "idle"
	// StateRequested is a request this server wrote that the host has not
	// picked up yet.
	StateRequested State = "requested"
	// StateRunning is a run in flight.
	StateRunning State = "running"
	// StateSucceeded is a run that applied a release.
	StateSucceeded State = "succeeded"
	// StateFailed is a run that did not finish.
	StateFailed State = "failed"
	// StateRolledBack is a release that was applied, did not come up healthy,
	// and was replaced by the one before it. It is not a kind of failure: the
	// instance is fine and the release is not.
	StateRolledBack State = "rolled_back"
	// StateRefused is the updater declining on purpose.
	StateRefused State = "refused"
	// StateUnknown is a host that has never written a status file, and what an
	// unreadable one is reported as.
	StateUnknown State = "unknown"
)

// Channel is which releases the host applies.
type Channel string

const (
	// ChannelSecurity takes patch releases of the installed MAJOR.MINOR only.
	// It is the default, here and on the host.
	ChannelSecurity Channel = "security"
	// ChannelAll takes every release.
	ChannelAll Channel = "all"
)

// Dir is the state directory. Its zero value is a deployment that has none,
// which is a supported install rather than a misconfiguration: every read
// answers that nothing is listening, and Request refuses.
type Dir struct{ path string }

// New returns the directory at path. An empty path is the feature switched
// off.
func New(path string) Dir { return Dir{path: strings.TrimSpace(path)} }

// Configured reports whether this install named a state directory at all.
func (d Dir) Configured() bool { return d.path != "" }

// Snapshot is the directory as it stands, in the terms the endpoint answers
// in. Every field has a meaning when nothing at all could be read, because
// having no files is the ordinary state of an install that never installed
// the host side.
type Snapshot struct {
	// Watching is whether something on the host is listening: a watcher stamp
	// that parses and is fresher than watcherFreshFor. It is what
	// self_update_available reports, and what a request is refused without.
	Watching bool
	// Channel is which releases the host applies. It is never a value outside
	// the two constants, whatever the file says.
	Channel Channel
	// State is what the last run did, or what the current one is doing.
	State State
	// AvailableVersion is the newest release the last check saw; empty is
	// none.
	AvailableVersion string
	// AvailableOutsideChannel is true when a release exists that the channel
	// will not apply.
	AvailableOutsideChannel bool
	// LastCheckAt and LastRunAt are zero when the host has never said.
	LastCheckAt time.Time
	LastRunAt   time.Time
	// Message is the updater's own last line, bounded and stripped of control
	// characters but otherwise verbatim.
	Message string
}

// Read is the whole picture the directory can give, as of now.
//
// It cannot fail. Every file it consults was written by another process, so
// each is either understood or reported as absent, and the defaults are the
// honest ones for an install where nothing has ever written anything: nothing
// is listening, the state is unknown, and the channel is the documented
// default.
func (d Dir) Read(now time.Time) Snapshot {
	snap := Snapshot{Channel: ChannelSecurity, State: StateUnknown}
	if !d.Configured() {
		return snap
	}

	if watcher, ok := d.watcher(); ok {
		// A stamp with no readable time is not a stamp: the whole claim is
		// that something was listening at a moment, so a moment nobody can
		// read fails closed.
		if seen := parseTime(watcher.LastSeen); !seen.IsZero() {
			snap.Watching = now.Sub(seen) < watcherFreshFor
		}
		snap.Channel = channelOr(watcher.Channel, snap.Channel)
	}

	status, haveStatus := d.status()
	if haveStatus {
		snap.Channel = channelOr(status.Channel, snap.Channel)
		snap.State = hostState(status.State)
		snap.AvailableVersion = status.AvailableVersion
		snap.AvailableOutsideChannel = status.AvailableOutsideChannel
		snap.LastCheckAt = parseTime(status.CheckedAt)
		snap.LastRunAt = lastRun(status)
		snap.Message = clip(status.Message)
	}

	// A request the host has not answered. It deletes the file before it acts
	// and echoes the id into the status it writes, so a request file whose id
	// no status names is one still waiting to be picked up — and an id that
	// cannot be read at all is treated the same way, because the file being
	// there is itself the evidence that somebody clicked.
	if req, ok := d.request(); ok && (!haveStatus || req.ID != status.ID) {
		snap.State = StateRequested
	}
	return snap
}

// Request asks the host to run kind, by writing the one file this server ever
// writes here. It answers as soon as the file is in place: this process does
// not wait on, and cannot see, whatever runs next.
//
// The file is published by rename because the reader is another process with
// no lock between them — it sees the whole previous request or the whole new
// one, never half of either — and it replaces any request already there,
// which is what makes a second click a repeat rather than a queue.
func (d Dir) Request(kind Kind, now time.Time) error {
	if !d.Configured() {
		return ErrNotConfigured
	}
	// The gate that makes "nothing else is ever written to this file" a
	// property of the code rather than of whoever calls it. Two literals
	// reach the file, and there is no path from any other string to it.
	switch kind {
	case KindCheck, KindApply:
	default:
		return fmt.Errorf("updatestate: %q is not a request kind", kind)
	}

	body, err := json.Marshal(requestJSON{
		Schema:      stateSchema,
		ID:          uuid.NewString(),
		Kind:        kind,
		RequestedAt: now.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("updatestate: encode the request: %w", err)
	}
	return d.publish(requestFile, body)
}

// The wire shapes of the three files.
//
// Timestamps are strings here and parsed one at a time (parseTime) rather
// than decoded into time.Time: a single malformed clock value would otherwise
// fail the whole file, and the run whose status an operator most needs to
// read is exactly the run that went wrong.
type (
	watcherJSON struct {
		Schema   int    `json:"schema"`
		LastSeen string `json:"last_seen"`
		Channel  string `json:"channel"`
	}

	requestJSON struct {
		Schema      int    `json:"schema"`
		ID          string `json:"id"`
		Kind        Kind   `json:"kind"`
		RequestedAt string `json:"requested_at"`
	}

	statusJSON struct {
		Schema                  int    `json:"schema"`
		ID                      string `json:"id"`
		State                   string `json:"state"`
		Channel                 string `json:"channel"`
		AvailableVersion        string `json:"available_version"`
		AvailableOutsideChannel bool   `json:"available_outside_channel"`
		StartedAt               string `json:"started_at"`
		FinishedAt              string `json:"finished_at"`
		CheckedAt               string `json:"checked_at"`
		Message                 string `json:"message"`
	}
)

func (d Dir) watcher() (watcherJSON, bool) {
	var f watcherJSON
	if !d.readFile(watcherFile, &f) || f.Schema != stateSchema {
		return watcherJSON{}, false
	}
	return f, true
}

func (d Dir) request() (requestJSON, bool) {
	var f requestJSON
	if !d.readFile(requestFile, &f) || f.Schema != stateSchema {
		return requestJSON{}, false
	}
	return f, true
}

func (d Dir) status() (statusJSON, bool) {
	var f statusJSON
	if !d.readFile(statusFile, &f) || f.Schema != stateSchema {
		return statusJSON{}, false
	}
	return f, true
}

// readFile decodes one file of the directory into dst, and reports false for
// every way that can fail to yield something this binary understands: it is
// not there, it cannot be opened, it is longer than maxStateFileBytes, it is
// not the JSON it claims to be, or something follows the document.
func (d Dir) readFile(name string, dst any) bool {
	// The path is the operator's own configuration joined with a constant.
	// Nothing a request carries reaches it.
	f, err := os.Open(filepath.Join(d.path, name)) // #nosec G304 -- operator configuration joined with a constant filename, never user input
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	// A file past the cap decodes to an unexpected EOF and is therefore
	// absent, which is the answer it deserves.
	dec := json.NewDecoder(io.LimitReader(f, maxStateFileBytes))
	if err = dec.Decode(dst); err != nil {
		return false
	}
	// A second document after the first is not a state file. The host writes
	// one object and publishes it by rename, so anything trailing means
	// something else wrote this — and reading the half that looks familiar is
	// how a dashboard reports a state nobody set.
	return !dec.More()
}

// publish writes body to name, atomically.
func (d Dir) publish(name string, body []byte) error {
	// The temporary file goes in the same directory, because a rename is only
	// atomic within one filesystem, and is dot-prefixed like the updater's own
	// (deploy/hamlaneh-update.sh) so that a half-written request can never be
	// mistaken for a request.
	tmp, err := os.CreateTemp(d.path, "."+name+".*")
	if err != nil {
		return fmt.Errorf("updatestate: create a temporary %s: %w", name, err)
	}
	path := tmp.Name()

	err = writeAndClose(tmp, body)
	if err == nil {
		err = os.Rename(path, filepath.Join(d.path, name))
	}
	if err != nil {
		// Nothing was published, so nothing may be left behind: a stray file
		// in a directory the host watches is litter at best.
		if rmErr := os.Remove(path); rmErr != nil {
			slog.Warn("remove a partial update request", "path", path, "error", rmErr)
		}
		return fmt.Errorf("updatestate: write %s: %w", name, err)
	}
	return nil
}

// writeAndClose writes body to f and closes it, reporting the first failure.
// The close is checked as carefully as the write: a file that closed with an
// error may be short, and a short request is one the host refuses.
func writeAndClose(f *os.File, body []byte) error {
	if _, err := f.Write(body); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			slog.Warn("close a partial update request", "path", f.Name(), "error", closeErr)
		}
		return err
	}
	return f.Close()
}

// channelOr keeps the reported channel inside its two values. What the file
// says was written by another process, and the field it feeds is an enum a
// client switches on; anything outside it becomes the documented default
// rather than a value no screen has a case for.
func channelOr(raw string, fallback Channel) Channel {
	switch Channel(raw) {
	case ChannelSecurity:
		return ChannelSecurity
	case ChannelAll:
		return ChannelAll
	}
	return fallback
}

// hostState maps what the status file says onto a state, and everything else
// onto StateUnknown.
func hostState(raw string) State {
	switch state := State(raw); state {
	case StateIdle, StateRunning, StateSucceeded, StateFailed, StateRolledBack, StateRefused:
		return state
	case StateRequested, StateUnknown:
		// Two words the host does not get to say. requested is this server's
		// own record of a click nothing has answered, and unknown is the
		// absence of an answer; a status file claiming either is as
		// meaningless as one claiming a word nobody defined.
		return StateUnknown
	}
	return StateUnknown
}

// lastRun is when the last run happened: when it finished, or when it started
// while it is still going.
func lastRun(s statusJSON) time.Time {
	if finished := parseTime(s.FinishedAt); !finished.IsZero() {
		return finished
	}
	return parseTime(s.StartedAt)
}

// parseTime reads one timestamp and answers the zero time for anything it
// cannot.
func parseTime(raw string) time.Time {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// clip bounds the host's own line and takes the teeth out of it. The text is
// carried verbatim except for two things: it is cut to maxMessageBytes, and
// C0 control characters other than newline and tab are dropped. Neither
// belongs in a log line, and both survive into places that read them as
// instructions rather than as text — a terminal, a log aggregator — long
// after the browser has rendered them harmlessly.
func clip(msg string) string {
	if len(msg) > maxMessageBytes {
		msg = msg[:maxMessageBytes]
		// The cut lands wherever the byte count did, so it may have split a
		// character. Half of one is not text.
		for len(msg) > 0 {
			r, size := utf8.DecodeLastRuneInString(msg)
			if r != utf8.RuneError || size > 1 {
				break
			}
			msg = msg[:len(msg)-1]
		}
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, msg)
}
