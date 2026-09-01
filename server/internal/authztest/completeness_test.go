package authztest

import (
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// specPath locates the API contract relative to this package.
const specPath = "../../../docs/api/openapi.yaml"

// channelSegment is the path segment that makes an operation channel-scoped.
const channelSegment = "{channelId}"

// requiredKinds are the fixture kinds ADR 002 requires of every {channelId}
// operation. DM answers genuinely differ — a fixed pair, no topic — and a DM
// is the most private object in the product, so proving the rules on a
// private channel alone proves half of them.
func requiredKinds() []storage.ChannelKind {
	return []storage.ChannelKind{storage.ChannelKindPrivate, storage.ChannelKindDM}
}

// authorshipOperations are the operations whose answer turns on who wrote the
// message. ADR 002 requires at least the MemberAuthor refinement on each, so
// the author's own outcome cannot be forgotten into silence.
func authorshipOperations() []Operation {
	return []Operation{
		{Method: http.MethodPatch, Path: messagePath},
		{Method: http.MethodDelete, Path: messagePath},
	}
}

// ownershipOperations are the operations whose answer turns on who made the
// resource. ADR 005 requires the ConferenceOwner refinement on each, so the
// owner's own outcome cannot be forgotten into silence — and without it the
// plain Member row would be asserting nothing about a stranger.
func ownershipOperations() []Operation {
	return []Operation{
		{Method: http.MethodDelete, Path: conferencePath},
	}
}

// probeFixture stands in for a provisioned cell where a test only needs to
// see that an entry's target resolves: well-formed ids that name nothing.
func probeFixture() Fixture {
	return Fixture{
		ChannelID:      "11111111-2222-3333-4444-555555555555",
		MessageID:      "bbbbbbbb-cccc-dddd-eeee-ffffffffffff",
		MemberUserID:   "66666666-7777-8888-9999-aaaaaaaaaaaa",
		OutsiderUserID: "cccccccc-dddd-eeee-ffff-000000000000",
		ConferenceID:   "99999999-8888-7777-6666-555555555555",
	}
}

// loadSpecOperations walks the paths section of openapi.yaml and returns
// every (method, path) operation.
func loadSpecOperations(t *testing.T) []Operation {
	t.Helper()

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}

	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	if len(doc.Paths) == 0 {
		t.Fatalf("%s has no paths; the parser or the contract is broken", specPath)
	}

	httpMethods := map[string]string{
		"get": http.MethodGet, "post": http.MethodPost, "put": http.MethodPut,
		"patch": http.MethodPatch, "delete": http.MethodDelete,
		"head": http.MethodHead, "options": http.MethodOptions, "trace": http.MethodTrace,
	}

	ops := []Operation{}
	for path, item := range doc.Paths {
		for key := range item {
			method, isMethod := httpMethods[strings.ToLower(key)]
			if !isMethod {
				continue // parameters, summary, etc.
			}
			ops = append(ops, Operation{Method: method, Path: path})
		}
	}
	return ops
}

// TestRegistryCoversSpec is the authz-completeness CI gate: every operation
// in openapi.yaml must have a matrix entry, and the registry must not carry
// entries for operations the contract no longer has.
func TestRegistryCoversSpec(t *testing.T) {
	t.Parallel()

	missing, extra := DiffRegistry(loadSpecOperations(t), Registry())
	if len(missing) > 0 {
		t.Errorf("endpoints in openapi.yaml without an authz matrix entry: %v\n"+
			"every endpoint must register its authorization expectations in internal/authztest (CLAUDE.md testing policy)",
			missing)
	}
	if len(extra) > 0 {
		t.Errorf("authz matrix entries without a matching endpoint in openapi.yaml: %v", extra)
	}
}

// TestRegistryEntriesAreComplete pins per-entry hygiene: an explicit
// classification, an expectation for every principal the entry's shape
// requires, and a target with no {template} segment left unfilled.
func TestRegistryEntriesAreComplete(t *testing.T) {
	t.Parallel()

	// One operation may hold several rows, one per fixture kind — but never
	// two rows of the same kind, which would be a copy-paste, not coverage.
	type row struct {
		Operation
		kind storage.ChannelKind
	}

	seen := map[row]bool{}
	for _, e := range Registry() {
		op := Operation{Method: e.Method, Path: e.Path}
		key := row{Operation: op, kind: e.Kind}
		if seen[key] {
			t.Errorf("%s registered twice for kind %q", op, e.Kind)
		}
		seen[key] = true

		if e.Class == ClassUnclassified {
			t.Errorf("%s has no security classification; classify it deliberately", op)
		}
		for _, principal := range e.RequiredPrincipals() {
			if _, ok := e.Want[principal]; !ok {
				t.Errorf("%s [%s] has no expectation for principal %s", op, e.Kind, principal)
			}
		}
		if target := e.Target(probeFixture()); strings.Contains(target, "{") {
			t.Errorf("%s request target %q still has a {template} segment; the fixture fills "+
				"{channelId}, {messageId} and {userId} — anything else needs a RequestTarget", op, target)
		}
	}
}

// TestChannelScopedEntryShape is the gate tightening ADR 002 asks for: an
// operation that acts on a channel cannot register the instance-scoped shape.
// Without it, the path of least resistance under a deadline is to declare the
// old four columns and dodge the five that carry this phase's whole IDOR
// story.
func TestChannelScopedEntryShape(t *testing.T) {
	t.Parallel()

	for _, e := range Registry() {
		op := Operation{Method: e.Method, Path: e.Path}
		actsOnChannel := strings.Contains(e.Path, channelSegment)

		switch {
		case actsOnChannel && !e.ChannelScoped():
			t.Errorf("%s names a %s but registers the instance-scoped entry shape; "+
				"give it a fixture Kind and declare the five channel relations (ADR 002)",
				op, channelSegment)
		case !actsOnChannel && e.ChannelScoped():
			t.Errorf("%s declares fixture kind %q, but its contract path names no channel",
				op, e.Kind)
		}
	}
}

// TestChannelOperationsCoverBothKinds enforces ADR 002's kind coverage: every
// {channelId} operation in the contract answers on a private channel and on a
// DM. A rule proven only on a private channel is proven for half the product.
func TestChannelOperationsCoverBothKinds(t *testing.T) {
	t.Parallel()

	covered := map[Operation]map[storage.ChannelKind]bool{}
	for _, e := range Registry() {
		if !e.ChannelScoped() {
			continue
		}
		op := Operation{Method: e.Method, Path: e.Path}
		if covered[op] == nil {
			covered[op] = map[storage.ChannelKind]bool{}
		}
		covered[op][e.Kind] = true
	}

	for _, op := range loadSpecOperations(t) {
		if !strings.Contains(op.Path, channelSegment) {
			continue
		}
		for _, kind := range requiredKinds() {
			if !covered[op][kind] {
				t.Errorf("%s has no %s fixture row; ADR 002 requires private and dm coverage "+
					"for every %s operation", op, kind, channelSegment)
			}
		}
	}
}

// TestPublicChannelTripwire pins the one public row ADR 002 names. In Phase
// 1.2 membership is the only visibility rule for every kind, so this cell is
// what turns red if somebody later opens public channels up without changing
// the contract first.
func TestPublicChannelTripwire(t *testing.T) {
	t.Parallel()

	for _, e := range Registry() {
		if e.Method != http.MethodGet || e.Path != channelPath || e.Kind != storage.ChannelKindPublic {
			continue
		}
		if got := e.Want[ChannelNonMember]; got != http.StatusNotFound {
			t.Errorf("getChannel on a public channel answers a non-member %d, want 404: "+
				"membership is the only visibility rule in this phase", got)
		}
		return
	}
	t.Errorf("no public-kind row for GET %s; ADR 002 pins one as the tripwire", channelPath)
}

// TestAuthorshipRefinements enforces the other half of ADR 002's refinement
// rule: the runner executes exactly the keys present in Want, so an operation
// that distinguishes the author must declare MemberAuthor or the author's own
// outcome is asserted nowhere.
func TestAuthorshipRefinements(t *testing.T) {
	t.Parallel()

	needsAuthor := map[Operation]bool{}
	for _, op := range authorshipOperations() {
		needsAuthor[op] = true
	}

	found := map[Operation]int{}
	for _, e := range Registry() {
		op := Operation{Method: e.Method, Path: e.Path}
		if !needsAuthor[op] {
			continue
		}
		found[op]++
		if _, ok := e.Want[MemberAuthor]; !ok {
			t.Errorf("%s [%s] does not declare %s; its contract answer turns on authorship "+
				"(ADR 002 requires the refinement)", op, e.Kind, MemberAuthor)
		}
	}
	for op := range needsAuthor {
		if found[op] == 0 {
			t.Errorf("%s distinguishes the message author but has no matrix entry at all", op)
		}
	}
}

// TestOwnershipRefinements is TestAuthorshipRefinements for the resource
// whose authority is ownership. It also pins the half that makes the
// refinement worth having: a row that declares ConferenceOwner but lets the
// plain Member column succeed would prove nothing about a stranger, and the
// contract's answer to a stranger is a 404 that never says a conference
// exists.
func TestOwnershipRefinements(t *testing.T) {
	t.Parallel()

	needsOwner := map[Operation]bool{}
	for _, op := range ownershipOperations() {
		needsOwner[op] = true
	}

	found := map[Operation]int{}
	for _, e := range Registry() {
		op := Operation{Method: e.Method, Path: e.Path}
		if !needsOwner[op] {
			continue
		}
		found[op]++
		if _, ok := e.Want[ConferenceOwner]; !ok {
			t.Errorf("%s does not declare %s; its contract answer turns on who made the "+
				"conference (ADR 005 requires the refinement)", op, ConferenceOwner)
		}
		if got := e.Want[Member]; got != http.StatusNotFound {
			t.Errorf("%s answers a non-owning member %d, want 404: a distinct refusal would "+
				"confirm the conference exists", op, got)
		}
	}
	for op := range needsOwner {
		if found[op] == 0 {
			t.Errorf("%s distinguishes the owner but has no matrix entry at all", op)
		}
	}
}

// TestNotImplementedExpectationsAreListed is the gate on 501 expectations:
// only the operations named in notImplementedOperations may have one.
//
// A 501 in the matrix is an honest statement while a handler does not exist —
// and a lie the moment it does, since the row would then assert the stub's
// answer for code that answers something else. Pinning the set to an explicit
// list makes leaving a cell there a deliberate edit, and makes the list
// impossible to forget: it fails when a row expects 501 without being listed,
// and it fails when a listed operation has no 501 left to justify it.
//
// The case this test cannot see — a handler that lands while its row still
// says 501 — is caught by TestAuthzMatrix, which asks the real server.
func TestNotImplementedExpectationsAreListed(t *testing.T) {
	t.Parallel()

	unlisted, stale := DiffNotImplemented(notImplementedOperations(), Registry())
	if len(unlisted) > 0 {
		t.Errorf("matrix cells expecting 501 for operations that are not in "+
			"notImplementedOperations: %v\na 501 expectation says nobody has written the "+
			"handler — if that is true, list the operation there deliberately; if it is not, "+
			"tighten the row to the contract's real answer", unlisted)
	}
	if len(stale) > 0 {
		t.Errorf("operations listed in notImplementedOperations that no cell expects 501 for: "+
			"%v\ntheir handlers have landed — drop them from the list", stale)
	}
}

// TestDiffNotImplementedDetectsBothDirections is the permanent form of the
// doctor-the-input-and-watch-it-fail drill, the same one
// TestDiffRegistryDetectsRemovals runs for the completeness gate: a gate
// nobody has watched trip is a gate nobody knows works.
func TestDiffNotImplementedDetectsBothDirections(t *testing.T) {
	t.Parallel()

	// A 501 expectation that nothing licenses. The row is built here rather
	// than taken from the registry, which no longer holds a single 501 cell:
	// every contract operation has a handler now, and a drill that could
	// only run while some stub happened to exist would have stopped
	// exercising this gate the moment the last one landed.
	stubbed := Entry{
		Method: http.MethodPost,
		Path:   "/api/v1/nothing",
		Class:  ClassSession,
		Want:   map[Principal]int{Member: http.StatusNotImplemented},
	}
	unlisted, _ := DiffNotImplemented(nil, []Entry{stubbed})
	if len(unlisted) == 0 {
		t.Error("an empty allow-list reported no unlisted 501 cells; the gate cannot see them")
	}

	// The other direction: an operation whose handler shipped long ago is
	// listed as not implemented. Nothing expects a 501 for it, so the list
	// entry is stale.
	landed := Operation{Method: http.MethodGet, Path: "/api/v1/channels"}
	_, stale := DiffNotImplemented(append(notImplementedOperations(), landed), Registry())
	if !slices.Contains(stale, landed.String()) {
		t.Errorf("listing the implemented %s was not reported stale; stale = %v", landed, stale)
	}
}

// TestRegistryClassifications pins the deliberate classifications ROADMAP
// 1.1 calls out: refresh is refresh-cookie-gated (anonymous 401, its own
// row semantics), and login plus the health probes are public.
func TestRegistryClassifications(t *testing.T) {
	t.Parallel()

	byOp := map[Operation]Entry{}
	for _, e := range Registry() {
		byOp[Operation{Method: e.Method, Path: e.Path}] = e
	}

	refresh, ok := byOp[Operation{Method: http.MethodPost, Path: "/api/v1/auth/refresh"}]
	if !ok {
		t.Fatal("refresh endpoint missing from the registry")
	}
	if refresh.Class != ClassRefreshCookie {
		t.Errorf("refresh classified %v, want ClassRefreshCookie — it is gated by the refresh cookie, not anonymous", refresh.Class)
	}
	if refresh.Want[Anonymous] != http.StatusUnauthorized {
		t.Errorf("refresh anonymous expectation %d, want 401", refresh.Want[Anonymous])
	}

	for _, public := range []Operation{
		{Method: http.MethodGet, Path: "/healthz"},
		{Method: http.MethodGet, Path: "/readyz"},
		{Method: http.MethodPost, Path: "/api/v1/auth/login"},
	} {
		e, ok := byOp[public]
		if !ok {
			t.Errorf("%s missing from the registry", public)
			continue
		}
		if e.Class != ClassPublic {
			t.Errorf("%s classified %v, want ClassPublic", public, e.Class)
		}
	}
}

// TestDiffRegistryDetectsRemovals proves the completeness gate trips: a
// registry missing one spec operation must be reported. This is the
// permanent form of the remove-one-entry-and-watch-it-fail drill.
func TestDiffRegistryDetectsRemovals(t *testing.T) {
	t.Parallel()

	// The instance-scoped rows come first and hold exactly one entry each, so
	// dropping the first really does leave its operation uncovered — a
	// channel-scoped row has a sibling of another kind that would cover for it.
	full := Registry()
	truncated := full[1:]
	dropped := Operation{Method: full[0].Method, Path: full[0].Path}
	if full[0].ChannelScoped() {
		t.Fatalf("%s is channel-scoped; this drill needs a single-entry operation first in the registry", dropped)
	}

	missing, _ := DiffRegistry(loadSpecOperations(t), truncated)
	found := false
	for _, m := range missing {
		if m == dropped.String() {
			found = true
		}
	}
	if !found {
		t.Errorf("removing %s from the registry was not detected; missing = %v", dropped, missing)
	}

	// And a registry entry the spec does not know is reported as extra.
	bogus := append([]Entry{}, full...)
	bogus = append(bogus, Entry{Method: http.MethodDelete, Path: "/api/v1/no-such-endpoint", Class: ClassPublic})
	_, extra := DiffRegistry(loadSpecOperations(t), bogus)
	if len(extra) != 1 || extra[0] != "DELETE /api/v1/no-such-endpoint" {
		t.Errorf("bogus registry entry not reported; extra = %v", extra)
	}
}
