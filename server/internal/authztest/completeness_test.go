package authztest

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// specPath locates the API contract relative to this package.
const specPath = "../../../docs/api/openapi.yaml"

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
// classification and an expectation for every principal.
func TestRegistryEntriesAreComplete(t *testing.T) {
	t.Parallel()

	seen := map[Operation]bool{}
	for _, e := range Registry() {
		op := Operation{Method: e.Method, Path: e.Path}
		if seen[op] {
			t.Errorf("%s registered twice", op)
		}
		seen[op] = true

		if e.Class == ClassUnclassified {
			t.Errorf("%s has no security classification; classify it deliberately", op)
		}
		for _, principal := range Principals() {
			if _, ok := e.Want[principal]; !ok {
				t.Errorf("%s has no expectation for principal %s", op, principal)
			}
		}
		if strings.Contains(e.Target(), "{") {
			t.Errorf("%s request target %q still has a {template} segment; "+
				"set RequestTarget to a concrete path so the harness exercises the route", op, e.Target())
		}
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

	full := Registry()
	truncated := full[1:] // drop one entry
	dropped := Operation{Method: full[0].Method, Path: full[0].Path}

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
