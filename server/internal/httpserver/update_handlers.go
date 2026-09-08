package httpserver

import (
	"net/http"
	"regexp"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/updatestate"
)

// The admin dashboard's update control (ADR 016).
//
// Neither handler here updates anything, and neither runs a command. The read
// reports three files a host process writes; the write publishes a fourth
// that the same process picks up, carrying a `check` or an `apply` and
// nothing else. The authority to replace a running image stays on the host,
// which is what lets the server container keep every restriction it has.
//
// Who may call these is decided before either runs: both routes are
// classAdmin in routePolicies, so securityMiddleware has already made the one
// authz.Can call by the time a handler is entered (CLAUDE.md: handlers never
// inline their own permission logic).

// defaultVersion is what a server nobody told answers with. It is the same
// word main.go carries for a binary the release workflow never stamped, so a
// test fixture and a git-checkout install say the same honest thing — and
// both report updatable false, which is the fail-closed direction.
const defaultVersion = "dev"

// releaseVersion matches a version this install could be updated FROM.
//
// The anti-rollback check compares the offered release against the installed
// one, so an installed version it cannot parse leaves it nothing to compare
// against, and the updater refuses the run rather than guessing. That refusal
// is the one control standing between a signed old release and this instance,
// so the dashboard reports it as its own state instead of offering a button
// that would fail (openapi.yaml, UpdateStatus.updatable).
//
// It is deliberately narrower than the host's own check, which accepts a bare
// 1.2.3 as well as v1.2.3 (deploy/verify-release.sh, version_is_valid). The
// tag the release workflow stamps carries the v, and erring towards
// "updatable false" only ever under-promises: it cannot make the dashboard
// offer an update the host would then refuse.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// WithVersion tells the server what release this binary was built as. It is
// the same string `hamlaneh-server --version` prints, and the updater reads
// that one to decide whether a release is newer, so the two must not be able
// to disagree — main.go passes the one variable both come from.
//
// Omitting it leaves the honest answer for a build nobody stamped.
func WithVersion(v string) Option {
	return func(s *apiServer) {
		if v != "" {
			s.version = v
		}
	}
}

// WithUpdateState names the directory this server shares with the host's
// updater (ADR 016). Omitting it — the default, and every install whose host
// has no watcher — reports self_update_available false and refuses a request
// with 503, which is the honest state of an instance nothing is listening to.
func WithUpdateState(dir string) Option {
	return func(s *apiServer) { s.updates = updatestate.New(dir) }
}

// GetUpdateStatus reports what this instance runs and what the host last saw.
//
// Everything but the installed version comes from files another process
// wrote, and reading them cannot fail: a directory that is not there, or one
// holding nothing this binary understands, is reported as a host that has
// never said anything rather than as a broken endpoint. An operator opens
// this screen precisely when something is wrong, and a 500 here would take
// away the one place that could have told them what.
func (s *apiServer) GetUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSONValue(w, r, http.StatusOK, s.updateStatus(time.Now()))
}

// RequestUpdate writes the request the host's watcher picks up, and answers
// with the status as it now stands — which is `requested` until the host gets
// to it.
//
// The body carries a kind and nothing else, and that is the security argument
// rather than a simplification: a body that could name a version would grow a
// force flag, and a force flag is a downgrade to any signed release with a
// known vulnerability (ADR 016 §1). The widest outcome available here is the
// release the host was going to apply within six hours anyway.
func (s *apiServer) RequestUpdate(w http.ResponseWriter, r *http.Request) {
	var req api.RequestUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Kind.Valid() {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"kind must be one of: check, apply")
		return
	}

	now := time.Now()
	status := s.updateStatus(now)
	// The same condition the GET publishes, asked again here rather than
	// trusted from the client: a request nothing will read is worse than a
	// refusal, because the operator would believe it was picked up.
	if !status.SelfUpdateAvailable {
		writeError(w, r, http.StatusServiceUnavailable, codeSelfUpdateUnavailable,
			"nothing on this host is listening for update requests")
		return
	}
	switch status.State {
	case api.Requested, api.Running:
		// A run in flight, or a click that has not been picked up yet.
		// Overwriting the request file would lose the first id and leave the
		// dashboard unable to tell whose run it is watching; the updater
		// takes a host-wide lock anyway, so a second run is not available to
		// grant.
		writeError(w, r, http.StatusConflict, codeUpdateInProgress,
			"an update run is already in flight")
		return
	case api.Idle, api.Succeeded, api.Failed, api.RolledBack, api.Refused, api.Unknown:
	}

	kind, action := updatestate.KindCheck, "update.check_requested"
	if req.Kind == api.Apply {
		kind, action = updatestate.KindApply, "update.apply_requested"
	}
	if err := s.updates.Request(kind, now); err != nil {
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{Action: action})
	// Read again rather than assuming: the host's watcher may already have
	// taken the request, and the answer should say what is true now.
	writeJSONValue(w, r, http.StatusAccepted, s.updateStatus(time.Now()))
}

// updateStatus assembles the contract's UpdateStatus. The first two fields
// come from this binary and can never come from a file — what is installed is
// something the server knows about itself, and taking it from the state
// directory would let whatever wrote there decide what this instance claims
// to be running.
func (s *apiServer) updateStatus(now time.Time) api.UpdateStatus {
	snap := s.updates.Read(now)
	outside := snap.AvailableOutsideChannel
	out := api.UpdateStatus{
		InstalledVersion:    s.version,
		Updatable:           releaseVersion.MatchString(s.version),
		SelfUpdateAvailable: snap.Watching,
		Channel:             api.UpdateStatusChannel(snap.Channel),
		State:               api.UpdateState(snap.State),
		// Always sent, like the counts on the settings screen: the field is a
		// definite yes or no about the release named beside it, and an absent
		// one reads as undefined on a screen that has to choose a sentence.
		AvailableOutsideChannel: &outside,
	}
	if snap.AvailableVersion != "" {
		version := snap.AvailableVersion
		out.AvailableVersion = &version
	}
	if !snap.LastCheckAt.IsZero() {
		checked := snap.LastCheckAt
		out.LastCheckAt = &checked
	}
	if !snap.LastRunAt.IsZero() {
		ran := snap.LastRunAt
		out.LastRunAt = &ran
	}
	if snap.Message != "" {
		message := snap.Message
		out.Message = &message
	}
	return out
}
