package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
)

// TestEveryContractRouteHasABudgetDecision is the rate-limit table's
// completeness gate, and the reason its fail-closed default is safe to have:
// every route the generated router registers must carry a deliberate budget
// decision, and the table must not name routes the contract no longer has.
//
// Without it, an endpoint added to openapi.yaml would reach production as a
// 500 rather than as an unbudgeted endpoint. With it, the omission is a red
// build.
func TestEveryContractRouteHasABudgetDecision(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{}
	var undeclared []string
	for _, pattern := range contractPatterns(t) {
		registered[pattern] = true
		if name, ok := endpointBudgets[pattern]; !ok || name == budgetUndeclared {
			undeclared = append(undeclared, pattern)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("contract routes with no entry in endpointBudgets: %v\n"+
			"decide every route deliberately — budgetNone is a decision, an omission is not", undeclared)
	}

	var stale []string
	for pattern := range endpointBudgets {
		if !registered[pattern] {
			stale = append(stale, pattern)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("endpointBudgets entries with no matching contract route: %v", stale)
	}
}

// TestEveryNamedBudgetResolves catches the fail-OPEN typo the other tests
// cannot see. A table entry naming a budget with no spec — budgetName("serach")
// — misses the limiter map and falls through to "no budget", so the endpoint
// would quietly serve unbudgeted while every route still answered 200.
//
// The reverse direction matters too: a spec nothing names is a budget that
// was written and never wired up.
func TestEveryNamedBudgetResolves(t *testing.T) {
	t.Parallel()

	// Deliberately not a switch: budgetName is open by design, and a switch
	// over it would make every new budget an edit to this test.
	named := map[budgetName]bool{}
	for pattern, name := range endpointBudgets {
		if name == budgetUndeclared {
			t.Errorf("%s names the empty budget; use budgetNone to leave it unbudgeted", pattern)
			continue
		}
		if name == budgetElsewhere || name == budgetNone {
			continue
		}
		if _, ok := budgetSpecs[name]; !ok {
			t.Errorf("%s names budget %q, which has no spec — the endpoint would serve unbudgeted",
				pattern, name)
		}
		named[name] = true
	}

	for name := range budgetSpecs {
		if !named[name] {
			t.Errorf("budget %q has a spec but no endpoint names it", name)
		}
	}
}

// TestEveryBudgetHasALimiter pins the wiring: newBudgetLimiters must build one
// limiter per spec, because a spec with no limiter is a budget the middleware
// silently skips.
func TestEveryBudgetHasALimiter(t *testing.T) {
	t.Parallel()

	limiters := newAPIServer(nil).budgets
	if len(limiters) != len(budgetSpecs) {
		t.Errorf("got %d limiters for %d specs", len(limiters), len(budgetSpecs))
	}
	for name := range budgetSpecs {
		if limiters[name] == nil {
			t.Errorf("budget %q has no limiter", name)
		}
	}
}

// TestEveryBudgetedRouteIsSessionGated pins the two tables against each
// other: a per-account budget has no account to key on outside a
// session-gated route, and would answer 500 on every call. The one budget
// declared perIP is the exception the invariant is stated around — and this
// test holds it to the other half of the deal, that a perIP budget is only
// ever put on a route with no session to key on instead.
func TestEveryBudgetedRouteIsSessionGated(t *testing.T) {
	t.Parallel()

	for pattern, name := range endpointBudgets {
		if name == budgetElsewhere || name == budgetNone || name == budgetUndeclared {
			continue
		}
		sessionGated := false
		switch routePolicies[pattern].class {
		case classSession, classSessionMustChangeAllowed, classAdmin:
			sessionGated = true
		case classUnclassified, classPublic, classRefreshCookie, classChallengeCookie:
		}

		switch {
		case budgetSpecs[name].perIP && sessionGated:
			t.Errorf("%s is session-gated but carries the per-IP budget %q; an authenticated "+
				"route must be keyed on the account, not on a shared address", pattern, name)
		case !budgetSpecs[name].perIP && !sessionGated:
			t.Errorf("%s carries budget %q but is not session-gated; a per-account budget "+
				"has no account to key on there", pattern, name)
		}
	}
}

// TestUndeclaredBudgetFailsClosed is the permanent form of the
// remove-one-entry-and-watch-it-refuse drill for this table: the middleware
// must refuse a request whose matched pattern carries no budget decision, and
// must never call the handler behind it.
func TestUndeclaredBudgetFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
	}{
		{name: "pattern absent from the budget table", pattern: "GET /api/v1/not-budgeted/{id}"},
		{name: "request never matched a route pattern", pattern: ""},
		{name: "concrete path of a parameterised route", pattern: "POST /api/v1/channels/abc/messages"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

			req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
			req.Pattern = tt.pattern
			rec := httptest.NewRecorder()
			newAPIServer(nil).rateLimitMiddleware(next).ServeHTTP(rec, req)

			if reached {
				t.Error("a route with no budget decision reached its handler; the middleware failed open")
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

// TestBudgetedRouteWithoutAPrincipalFailsClosed covers the other disagreement
// between the two tables: a route budgeted per account that somehow arrives
// with no principal. Serving it would drop the budget silently, so it answers
// 500 instead.
func TestBudgetedRouteWithoutAPrincipalFailsClosed(t *testing.T) {
	t.Parallel()

	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=ab", nil)
	req.Pattern = "GET /api/v1/search"
	rec := httptest.NewRecorder()
	newAPIServer(nil).rateLimitMiddleware(next).ServeHTTP(rec, req)

	if reached {
		t.Error("a per-account budget with no account reached its handler unbudgeted")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got status %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestUnbudgetedRouteReachesItsHandler is the positive half of the pair above:
// a route the table deliberately leaves unbudgeted is served, so the
// fail-closed tests prove refusal rather than a middleware that refuses
// everything.
func TestUnbudgetedRouteReachesItsHandler(t *testing.T) {
	t.Parallel()

	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Pattern = "GET /healthz"
	rec := httptest.NewRecorder()
	newAPIServer(nil).rateLimitMiddleware(next).ServeHTTP(rec, req)

	if !reached {
		t.Errorf("an unbudgeted route did not reach its handler (status %d, body %s)",
			rec.Code, rec.Body.String())
	}
}
