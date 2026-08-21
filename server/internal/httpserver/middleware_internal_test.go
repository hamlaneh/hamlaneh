package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
)

// patternRecorder is an api.ServeMux that records every pattern the
// generated router registers before delegating to a real ServeMux. It is
// how the tests below learn the contract's route set without re-parsing
// openapi.yaml: the generated code is the spec, compiled.
type patternRecorder struct {
	*http.ServeMux
	patterns []string
}

func (p *patternRecorder) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	p.patterns = append(p.patterns, pattern)
	p.ServeMux.HandleFunc(pattern, handler)
}

// contractPatterns returns every route pattern the generated api package
// registers, in registration order.
func contractPatterns(t *testing.T) []string {
	t.Helper()

	rec := &patternRecorder{ServeMux: http.NewServeMux()}
	api.HandlerWithOptions(newAPIServer(nil), api.StdHTTPServerOptions{BaseRouter: rec})
	if len(rec.patterns) == 0 {
		t.Fatal("the generated router registered no routes; the recorder or the codegen is broken")
	}
	return rec.patterns
}

// TestEveryContractRouteIsClassified is the route-policy completeness gate:
// every route the generated router registers must carry a deliberate
// security classification, and the table must not classify routes the
// contract no longer has. Without it, an endpoint added to openapi.yaml
// would reach production as a fail-closed 500 instead of working.
func TestEveryContractRouteIsClassified(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{}
	var unclassified []string
	for _, pattern := range contractPatterns(t) {
		registered[pattern] = true
		if pol, ok := routePolicies[pattern]; !ok || pol.class == classUnclassified {
			unclassified = append(unclassified, pattern)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("contract routes with no entry in routePolicies: %v\n"+
			"classify every route deliberately — an unclassified route fails closed with 500", unclassified)
	}

	var stale []string
	for pattern := range routePolicies {
		if !registered[pattern] {
			stale = append(stale, pattern)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("routePolicies entries with no matching contract route: %v", stale)
	}
}

// TestUnclassifiedPatternFailsClosed is the permanent form of the
// remove-one-entry-and-watch-it-refuse drill: the middleware must refuse a
// request whose matched pattern it cannot classify, and must never call the
// handler behind it. The cases cover both ways that happens — a pattern
// nobody registered a policy for, and a request that reached the middleware
// without ever being matched by a ServeMux (empty Pattern).
func TestUnclassifiedPatternFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
	}{
		{name: "pattern absent from the policy table", pattern: "GET /api/v1/not-classified/{id}"},
		{name: "request never matched a route pattern", pattern: ""},
		{name: "concrete path of a parameterised route", pattern: "GET /api/v1/channels/abc"},
		{name: "classified path with an unclassified method", pattern: "DELETE /api/v1/users/me"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

			req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
			req.Pattern = tt.pattern
			rec := httptest.NewRecorder()
			newAPIServer(nil).securityMiddleware(next).ServeHTTP(rec, req)

			if reached {
				t.Error("unclassified route reached its handler; the middleware failed open")
			}
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("got status %d, want 500 (body %s)", rec.Code, rec.Body.String())
			}

			var body api.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not the contract Error shape: %v", rec.Body.String(), err)
			}
			if body.Error.Code != string(codeInternalError) {
				t.Errorf("got error code %q, want %q", body.Error.Code, codeInternalError)
			}
		})
	}
}

// TestClassifiedPatternReachesHandler is the positive half of the fail-closed
// pair: a classified public pattern does reach the handler, so the test above
// proves refusal rather than a middleware that refuses everything.
func TestClassifiedPatternReachesHandler(t *testing.T) {
	t.Parallel()

	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Pattern = "GET /healthz"
	rec := httptest.NewRecorder()
	newAPIServer(nil).securityMiddleware(next).ServeHTTP(rec, req)

	if !reached {
		t.Errorf("classified public route did not reach its handler (status %d, body %s)", rec.Code, rec.Body.String())
	}
}
