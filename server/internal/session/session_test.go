package session

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewToken(t *testing.T) {
	t.Parallel()

	raw, hash := NewToken()

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("token %q is not base64url: %v", raw, err)
	}
	if len(decoded) != 32 {
		t.Errorf("token carries %d bytes of entropy, want 32", len(decoded))
	}

	want := sha256.Sum256([]byte(raw))
	if string(hash) != string(want[:]) {
		t.Error("returned hash is not the SHA-256 of the raw token")
	}

	again, _ := NewToken()
	if raw == again {
		t.Error("two tokens are identical; generation is not random")
	}
}

func TestHashToken(t *testing.T) {
	t.Parallel()

	if len(HashToken("anything")) != 32 {
		t.Error("HashToken digest is not 32 bytes")
	}
	if string(HashToken("a")) == string(HashToken("b")) {
		t.Error("different tokens hash identically")
	}
	// Total function: arbitrary cookie garbage still hashes.
	if len(HashToken("!!not base64!!")) != 32 {
		t.Error("HashToken failed on a non-base64 value")
	}
}

func TestNewCSRFToken(t *testing.T) {
	t.Parallel()

	tok := NewCSRFToken()
	decoded, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		t.Fatalf("csrf token %q is not base64url: %v", tok, err)
	}
	if len(decoded) != 32 {
		t.Errorf("csrf token carries %d bytes of entropy, want 32", len(decoded))
	}
}

func TestSetCookies(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	SetCookies(rec, Cookies("acc", "ref", "csrf"))

	got := rec.Result().Cookies()
	if len(got) != 3 {
		t.Fatalf("response carries %d cookies, want 3", len(got))
	}
	names := map[string]bool{}
	for _, c := range got {
		names[c.Name] = true
	}
	for _, want := range []string{AccessCookie, RefreshCookie, CSRFCookie} {
		if !names[want] {
			t.Errorf("response is missing cookie %s", want)
		}
	}
}

func TestValidCSRF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cookie string
		header string
		want   bool
	}{
		{"matching values", "tok123", "tok123", true},
		{"mismatched values", "tok123", "tok456", false},
		{"empty header", "tok123", "", false},
		{"empty cookie", "", "tok123", false},
		{"both empty", "", "", false},
		{"prefix is not a match", "tok123", "tok12", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidCSRF(tt.cookie, tt.header); got != tt.want {
				t.Errorf("ValidCSRF(%q, %q) = %v, want %v", tt.cookie, tt.header, got, tt.want)
			}
		})
	}
}

// wantCookie is the exact attribute set the security design pins for each
// session cookie.
type wantCookie struct {
	name     string
	path     string
	maxAge   int
	httpOnly bool
}

func assertCookies(t *testing.T, got []*http.Cookie, want []wantCookie) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d cookies, want %d", len(got), len(want))
	}
	for i, w := range want {
		c := got[i]
		if c.Name != w.name {
			t.Errorf("cookie %d name = %q, want %q", i, c.Name, w.name)
		}
		if c.Path != w.path {
			t.Errorf("cookie %s path = %q, want %q", c.Name, c.Path, w.path)
		}
		if c.MaxAge != w.maxAge {
			t.Errorf("cookie %s max-age = %d, want %d", c.Name, c.MaxAge, w.maxAge)
		}
		if c.HttpOnly != w.httpOnly {
			t.Errorf("cookie %s httponly = %v, want %v", c.Name, c.HttpOnly, w.httpOnly)
		}
		if !c.Secure {
			t.Errorf("cookie %s is not Secure", c.Name)
		}
		if c.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie %s samesite = %v, want Strict", c.Name, c.SameSite)
		}
	}
}

func TestCookies(t *testing.T) {
	t.Parallel()

	got := Cookies("acc", "ref", "csrf")
	assertCookies(t, got, []wantCookie{
		{name: "hamlaneh_session", path: "/", maxAge: 900, httpOnly: true},
		{name: "hamlaneh_refresh", path: "/api/v1/auth/refresh", maxAge: 2592000, httpOnly: true},
		{name: "hamlaneh_csrf", path: "/", maxAge: 2592000, httpOnly: false},
	})
	for i, val := range []string{"acc", "ref", "csrf"} {
		if got[i].Value != val {
			t.Errorf("cookie %s value = %q, want %q", got[i].Name, got[i].Value, val)
		}
	}
}

func TestRotatedCookies(t *testing.T) {
	t.Parallel()

	got := RotatedCookies("acc2", "ref2")
	assertCookies(t, got, []wantCookie{
		{name: "hamlaneh_session", path: "/", maxAge: 900, httpOnly: true},
		{name: "hamlaneh_refresh", path: "/api/v1/auth/refresh", maxAge: 2592000, httpOnly: true},
	})
}

func TestClearCookies(t *testing.T) {
	t.Parallel()

	got := ClearCookies()
	assertCookies(t, got, []wantCookie{
		{name: "hamlaneh_session", path: "/", maxAge: -1, httpOnly: true},
		{name: "hamlaneh_refresh", path: "/api/v1/auth/refresh", maxAge: -1, httpOnly: true},
		{name: "hamlaneh_csrf", path: "/", maxAge: -1, httpOnly: false},
	})
	for _, c := range got {
		if c.Value != "" {
			t.Errorf("cleared cookie %s still has value %q", c.Name, c.Value)
		}
	}
}
