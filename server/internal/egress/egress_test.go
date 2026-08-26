package egress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeResolver answers from a table, falling back to parsing the host as an
// IP literal so a test can drive both name-based and literal targets.
// answers may hold more than one entry per host, and each lookup of that
// host consumes the next one — which is how DNS rebinding is expressed.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr
	lookups map[string]int
}

func newFakeResolver(answers map[string][][]netip.Addr) *fakeResolver {
	return &fakeResolver{answers: answers, lookups: map[string]int{}}
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lookups[host]++
	if rounds, ok := f.answers[host]; ok && len(rounds) > 0 {
		round := rounds[min(f.lookups[host]-1, len(rounds)-1)]
		return round, nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	return nil, fmt.Errorf("fakeResolver: no answer for %q", host)
}

func (f *fakeResolver) count(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups[host]
}

func addrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()

	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		out = append(out, addr)
	}
	return out
}

// anyPort lets a test reach an httptest server, which never binds 80 or 443.
func anyPort(string) bool { return true }

// loopbackAllowed is the real blocklist with one hole: the loopback address
// an httptest server binds. Everything else — including any address a test
// server redirects to — is still judged by the production rule.
func loopbackAllowed(addr netip.Addr) bool {
	if addr.Unmap().IsLoopback() {
		return false
	}
	return blocked(addr)
}

// TestBlockedCoversEveryPrivateRange pins the predicate itself, including
// the v4-mapped-v6 spelling of each v4 range: ::ffff:127.0.0.1 must be as
// refused as 127.0.0.1.
func TestBlockedCoversEveryPrivateRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.53.1.9", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"10.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"172.16.4.4", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"::ffff:169.254.169.254", true},
		{"fe80::1", true},
		{"fe80::dead:beef", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"224.0.0.1", true},
		{"ff02::1", true},
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"::", true},
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"100.128.0.1", false},
		{"2606:4700:4700::1111", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()

			addr, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := blocked(addr); got != tt.want {
				t.Errorf("blocked(%s) = %t, want %t", tt.addr, got, tt.want)
			}
		})
	}
}

// TestFetchRefusesPrivateTargets is the SSRF matrix as ROADMAP 1.3 lists it,
// driven through the whole client: whether the private address arrives as a
// DNS answer or as a literal in the URL, the fetch is refused.
func TestFetchRefusesPrivateTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		answer []string
	}{
		{"cloud metadata", "http://metadata.test/latest/meta-data/", []string{"169.254.169.254"}},
		{"rfc1918", "http://intranet.test/", []string{"10.0.0.7"}},
		{"loopback", "http://localhost.test/", []string{"127.0.0.1"}},
		{"ipv6 link local", "http://v6.test/", []string{"fe80::1"}},
		{"cgnat", "http://cgnat.test/", []string{"100.64.3.9"}},
		{"unique local", "http://ula.test/", []string{"fc00::5"}},
		{"v4 mapped v6", "http://mapped.test/", []string{"::ffff:10.0.0.7"}},
		{"every answer private", "http://both.test/", []string{"10.0.0.7", "192.168.0.1"}},
		{"ipv6 literal loopback", "http://[::1]/", nil},
		{"ipv6 literal link local", "http://[fe80::1]/", nil},
		{"ipv6 literal mapped loopback", "http://[::ffff:127.0.0.1]/", nil},
		{"ipv4 literal metadata", "http://169.254.169.254/", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			answers := map[string][][]netip.Addr{}
			if tt.answer != nil {
				host := mustHost(t, tt.target)
				answers[host] = [][]netip.Addr{addrs(t, tt.answer...)}
			}
			client := newClient(newFakeResolver(answers), blocked, anyPort)

			_, _, err := client.Fetch(t.Context(), tt.target, 1024)
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("Fetch(%s) error = %v, want ErrBlockedAddress", tt.target, err)
			}
		})
	}
}

// TestFetchDialsThePublicAnswerOfASplitHorizonName proves the loop skips a
// blocked address instead of failing on it: a name that answers with both a
// private and a public address is ordinary, and the public half is
// legitimately fetchable.
func TestFetchDialsThePublicAnswerOfASplitHorizonName(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	loopback := mustHost(t, server.URL)
	answers := map[string][][]netip.Addr{
		"split.test": {addrs(t, "10.0.0.1", loopback)},
	}
	client := newClient(newFakeResolver(answers), loopbackAllowed, anyPort)

	body, _, err := client.Fetch(t.Context(), "http://split.test:"+mustPort(t, server.URL)+"/", 1024)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

// TestFetchChecksTheAddressAtDialTime is the DNS-rebinding case.
//
// The resolver answers with a public address the first time it is asked and
// with loopback every time after. The test spends the first answer itself,
// standing in for the pre-flight check a naive implementation would do and
// then trust — and the fetch that follows still refuses, because the address
// it judges is the one it is about to dial, not the one that validated a
// moment ago.
func TestFetchChecksTheAddressAtDialTime(t *testing.T) {
	t.Parallel()

	res := newFakeResolver(map[string][][]netip.Addr{
		"rebind.test": {addrs(t, "93.184.216.34"), addrs(t, "127.0.0.1")},
	})

	preflight, err := res.LookupNetIP(t.Context(), "ip", "rebind.test")
	if err != nil {
		t.Fatalf("preflight lookup: %v", err)
	}
	if blocked(preflight[0]) {
		t.Fatalf("preflight answer %s should look public", preflight[0])
	}

	client := newClient(res, blocked, anyPort)
	if _, _, err = client.Fetch(t.Context(), "http://rebind.test/", 1024); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("Fetch after rebind: error = %v, want ErrBlockedAddress", err)
	}
	if got := res.count("rebind.test"); got != 2 {
		t.Errorf("resolver consulted %d times, want 2 (the preflight and the dial)", got)
	}
}

// TestFetchRefusesRedirectToPrivateAddress: the first hop is legitimate and
// the second is the attack. The guard runs per dial, so the redirect gets
// the same judgement the original URL got.
func TestFetchRefusesRedirectToPrivateAddress(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.7/secrets", http.StatusFound)
	}))
	defer server.Close()

	client := newClient(newFakeResolver(nil), loopbackAllowed, anyPort)

	_, _, err := client.Fetch(t.Context(), server.URL, 1024)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("Fetch through redirect: error = %v, want ErrBlockedAddress", err)
	}
}

// TestFetchStopsAfterMaxRedirects caps the chain. A server that redirects to
// itself forever must cost four requests, not a goroutine.
func TestFetchStopsAfterMaxRedirects(t *testing.T) {
	t.Parallel()

	var hops int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hops++
		mu.Unlock()
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer server.Close()

	client := newClient(newFakeResolver(nil), loopbackAllowed, anyPort)

	_, _, err := client.Fetch(t.Context(), server.URL, 1024)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("Fetch: error = %v, want ErrTooManyRedirects", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hops != MaxRedirects+1 {
		t.Errorf("server saw %d requests, want %d", hops, MaxRedirects+1)
	}
}

// TestNewRefusesPortsOtherThan80And443 drives the production client, whose
// port rule is the real one. Port 22 is refused before the resolver is
// asked, so an SSH probe costs neither a connection nor a DNS query.
func TestNewRefusesPortsOtherThan80And443(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"http://example.test:22/",
		"http://example.test:6379/",
		"https://example.test:5432/",
		"http://127.0.0.1:11211/",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			_, _, err := New().Fetch(t.Context(), target, 1024)
			if !errors.Is(err, ErrBlockedPort) {
				t.Fatalf("Fetch(%s) error = %v, want ErrBlockedPort", target, err)
			}
		})
	}
}

// TestStandardPortAllowsOnlyWebPorts pins the rule New installs.
func TestStandardPortAllowsOnlyWebPorts(t *testing.T) {
	t.Parallel()

	for port, want := range map[string]bool{"80": true, "443": true, "22": false, "8080": false, "0": false} {
		if got := standardPort(port); got != want {
			t.Errorf("standardPort(%s) = %t, want %t", port, got, want)
		}
	}
}

// TestFetchRefusesNonHTTPSchemes: file:// and gopher:// never reach a dialer.
func TestFetchRefusesNonHTTPSchemes(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"file:///etc/passwd", "gopher://example.test/", "ftp://example.test/"} {
		if _, _, err := New().Fetch(t.Context(), target, 1024); !errors.Is(err, ErrBlockedScheme) {
			t.Errorf("Fetch(%s) error = %v, want ErrBlockedScheme", target, err)
		}
	}
}

// TestFetchTruncatesAtTheLimit: the cap is enforced by the reader, so a
// server that lies about Content-Length changes nothing.
func TestFetchTruncatesAtTheLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 100_000)))
	}))
	defer server.Close()

	client := newClient(newFakeResolver(nil), loopbackAllowed, anyPort)

	body, _, err := client.Fetch(t.Context(), server.URL, 1024)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(body) != 1024 {
		t.Errorf("body = %d bytes, want 1024", len(body))
	}
}

// TestFetchRefusesNon200 keeps a 404 body out of a preview card.
func TestFetchRefusesNon200(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()

	client := newClient(newFakeResolver(nil), loopbackAllowed, anyPort)

	if _, _, err := client.Fetch(t.Context(), server.URL, 1024); !errors.Is(err, ErrNotOK) {
		t.Fatalf("Fetch: error = %v, want ErrNotOK", err)
	}
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return parsed.Hostname()
}

func mustPort(t *testing.T, rawURL string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return parsed.Port()
}
