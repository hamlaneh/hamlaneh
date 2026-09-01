package wsgateway

import "testing"

// TestLoopbackAliasAcceptsEveryLoopbackSpelling is home mode's one origin
// allowance (ADR 012). A household user types whichever spelling of "this
// machine" they think of, and localhost:8080 and 127.0.0.1:8080 are the same
// machine, the same port and the same trust boundary. Without this the page
// loads and every socket is refused: a chat that appears to work and receives
// nothing.
func TestLoopbackAliasAcceptsEveryLoopbackSpelling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured string
		origin     string
		want       bool
	}{
		{"the configured spelling", "http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"the name people type", "http://127.0.0.1:8080", "http://localhost:8080", true},
		{"IPv6 loopback", "http://127.0.0.1:8080", "http://[::1]:8080", true},
		{"case is ignored as everywhere else", "http://127.0.0.1:8080", "HTTP://LOCALHOST:8080", true},
		{"configured the other way round", "http://localhost:8080", "http://127.0.0.1:8080", true},
		{"configured on IPv6", "http://[::1]:8080", "http://localhost:8080", true},
		{"the default port survives normalization", "http://127.0.0.1:80", "http://localhost", true},

		// The allowance is loopback, on the bound port, over that scheme.
		// Nothing else moves.
		{"another port is another instance", "http://127.0.0.1:8080", "http://localhost:9090", false},
		{"no port when one was configured", "http://127.0.0.1:8080", "http://localhost", false},
		{"another scheme", "http://127.0.0.1:8080", "https://localhost:8080", false},
		{"a foreign site", "http://127.0.0.1:8080", "http://evil.example.com", false},
		{"a name that merely contains ours", "http://127.0.0.1:8080", "http://localhost.evil.net:8080", false},
		{"an address that merely starts with ours", "http://127.0.0.1:8080", "http://127.0.0.1.evil.net:8080", false},
		{"another host in 127/8 is not a spelling anyone types", "http://127.0.0.1:8080", "http://127.0.0.2:8080", false},
		{"missing", "http://127.0.0.1:8080", "", false},
		{"null", "http://127.0.0.1:8080", "null", false},
		{"wildcard", "http://127.0.0.1:8080", "*", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := New(newFakeStore(), tc.configured, WithLoopbackAlias())
			t.Cleanup(func() {
				if err := g.Close(); err != nil {
					t.Errorf("close gateway: %v", err)
				}
			})
			if got := g.OriginAllowed(tc.origin); got != tc.want {
				t.Errorf("home gateway on %q: OriginAllowed(%q) = %v, want %v",
					tc.configured, tc.origin, got, tc.want)
			}
		})
	}
}

// TestLoopbackAliasIsNotAppliedInServerMode is the regression guard that makes
// the allowance above safe to have: server mode never passes the option, and a
// gateway without it refuses the sibling spelling exactly as it always did.
func TestLoopbackAliasIsNotAppliedInServerMode(t *testing.T) {
	t.Parallel()

	g := New(newFakeStore(), "http://127.0.0.1:8080")
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})

	if !g.OriginAllowed("http://127.0.0.1:8080") {
		t.Error("a gateway refused its own configured origin")
	}
	for _, origin := range []string{"http://localhost:8080", "http://[::1]:8080"} {
		if g.OriginAllowed(origin) {
			t.Errorf("OriginAllowed(%q) = true without WithLoopbackAlias; the option is not opt-in", origin)
		}
	}
}

// TestLoopbackAliasChangesNothingOffLoopback is the stronger half of that
// guard: even if the option were somehow applied to a public instance, it adds
// no origin at all. It re-runs TestOriginAllowed's table with the option on and
// requires every answer to be identical, so the allowance cannot widen a
// deployment that is not on loopback.
func TestLoopbackAliasChangesNothingOffLoopback(t *testing.T) {
	t.Parallel()

	const configured = "https://chat.example.com"
	origins := []string{
		"https://chat.example.com", "HTTPS://CHAT.EXAMPLE.COM", "", "null",
		"http://chat.example.com", "https://evil.example.com", "https://a.chat.example.com",
		"https://example.com", "https://chat.example.com.evil.net", "https://chat.example.com:443",
		"https://chat.example.com/", "http://localhost:8080", "http://127.0.0.1:8080",
	}

	plain := New(newFakeStore(), configured)
	aliased := New(newFakeStore(), configured, WithLoopbackAlias())
	t.Cleanup(func() {
		if err := plain.Close(); err != nil {
			t.Errorf("close plain gateway: %v", err)
		}
		if err := aliased.Close(); err != nil {
			t.Errorf("close aliased gateway: %v", err)
		}
	})

	for _, origin := range origins {
		if want, got := plain.OriginAllowed(origin), aliased.OriginAllowed(origin); want != got {
			t.Errorf("WithLoopbackAlias changed OriginAllowed(%q) on a public instance: %v, want %v",
				origin, got, want)
		}
	}
}

// TestLoopbackAliasKeepsTheFailClosedDefault: an unconfigured gateway allows
// nothing, and asking for the alias must not turn "I cannot tell whether this
// is same-site" into a yes.
func TestLoopbackAliasKeepsTheFailClosedDefault(t *testing.T) {
	t.Parallel()

	g := New(newFakeStore(), "", WithLoopbackAlias())
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})

	for _, origin := range []string{"", "null", "http://localhost:8080", "http://127.0.0.1:8080", "*"} {
		if g.OriginAllowed(origin) {
			t.Errorf("OriginAllowed(%q) = true on an unconfigured gateway", origin)
		}
	}
}
