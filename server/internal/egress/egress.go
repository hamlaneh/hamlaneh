// Package egress provides the one HTTP client this server uses to fetch a
// URL somebody else chose. Today that is link previews; anything else that
// ever fetches a user-supplied URL goes through here too.
//
// The whole point is the rule from ADR 003: validate the IP that is dialed,
// not the name that was resolved. A check performed before the request
// ("resolve the host, decide it is public, then hand the name to
// net/http") is not a check at all — net/http resolves the name again when
// it dials, and a resolver that answers 93.184.216.34 to the first lookup
// and 127.0.0.1 to the second (DNS rebinding) walks straight past it. So
// the check lives inside the dialer: the address is resolved once, the
// resulting IP is tested, and the connection is made to that exact IP.
// There is no window between the decision and the connection for an answer
// to change in.
//
// Everything else here is a budget rather than a rule: 5 seconds total,
// ports 80 and 443 only, at most three redirects (each one re-dialed and so
// re-checked), and a caller-supplied byte cap on the response.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// Sentinel errors. Fetch wraps them, so callers test with errors.Is.
var (
	// ErrBlockedAddress reports that every address the host resolved to is
	// one this server refuses to dial.
	ErrBlockedAddress = errors.New("egress: address is not permitted")
	// ErrBlockedPort reports a port other than 80 or 443.
	ErrBlockedPort = errors.New("egress: only ports 80 and 443 are dialed")
	// ErrBlockedScheme reports a scheme other than http or https.
	ErrBlockedScheme = errors.New("egress: only http and https are fetched")
	// ErrTooManyRedirects reports a chain longer than MaxRedirects.
	ErrTooManyRedirects = errors.New("egress: too many redirects")
	// ErrNotOK reports a response status other than 200.
	ErrNotOK = errors.New("egress: response was not 200")
)

const (
	// Timeout bounds one whole fetch: connect, TLS, headers and body.
	Timeout = 5 * time.Second
	// MaxRedirects is the hop cap. Each hop is a fresh dial and so a fresh
	// address check; the cap is about loops and latency, not safety.
	MaxRedirects = 3

	// userAgent identifies the fetch honestly. Sites that do not want
	// preview bots can refuse it by name, which is the polite contract.
	userAgent = "Hamlaneh-LinkPreview/1.0 (+https://github.com/hamlaneh/hamlaneh)"
)

// cgnat and zeroNet are the two ranges the standard library's Addr
// predicates do not cover: RFC 6598 carrier-grade NAT, which routes to the
// provider's own network, and 0.0.0.0/8, which several stacks treat as
// "this host". Loopback, RFC 1918, 169.254/16, fe80::/10, fc00::/7,
// multicast and the unspecified address all have predicates already.
var (
	cgnat   = netip.MustParsePrefix("100.64.0.0/10")
	zeroNet = netip.MustParsePrefix("0.0.0.0/8")
)

// blocked reports whether an address is one this server refuses to dial.
//
// Unmap first: ::ffff:127.0.0.1 is 127.0.0.1 wearing a costume, and every
// predicate below would answer false for the v6 form of a v4 address that
// must be refused.
func blocked(addr netip.Addr) bool {
	addr = addr.Unmap()
	switch {
	case !addr.IsValid(),
		addr.IsUnspecified(),
		addr.IsLoopback(),
		addr.IsPrivate(),
		addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(),
		addr.IsMulticast():
		return true
	}
	return cgnat.Contains(addr) || zeroNet.Contains(addr)
}

// resolver is the DNS seam. *net.Resolver is the production one; tests
// substitute a resolver whose answers change between calls, which is how
// the rebinding case is exercised without a real zone.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

var _ resolver = (*net.Resolver)(nil)

// Client is a guarded HTTP client. Use New; the zero value is not usable.
type Client struct {
	httpc *http.Client
}

// standardPort is the production port rule: the two ports a web page is
// ever served from, and nothing else.
func standardPort(port string) bool { return port == "80" || port == "443" }

// New returns the guarded client: system resolver, real blocklist, ports
// 80 and 443.
func New() *Client {
	return newClient(net.DefaultResolver, blocked, standardPort)
}

// newClient builds a Client over an injectable resolver, address predicate
// and port rule. Only tests pass anything but the production three — a test
// needs to reach an httptest server, which binds a loopback address the real
// predicate refuses on a random high port the real rule refuses. The
// production rules are pinned directly, against New, by their own tests.
func newClient(res resolver, isBlocked func(netip.Addr) bool, portOK func(string) bool) *Client {
	return &Client{httpc: &http.Client{
		Transport: &http.Transport{
			DialContext: guardedDial(res, isBlocked, portOK),
			// No proxy: a proxy would turn every dial into a dial of the
			// proxy, and the address actually fetched would stop being the
			// address this guard checked.
			Proxy: nil,
			// One connection per fetch. Pooling would let a later fetch of
			// a different URL ride a connection opened for an earlier one.
			DisableKeepAlives:     true,
			TLSHandshakeTimeout:   Timeout,
			ResponseHeaderTimeout: Timeout,
		},
		Timeout: Timeout,
		// No cookie jar anywhere: this client carries no state between
		// fetches and no ambient authority into one.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return ErrTooManyRedirects
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: %s", ErrBlockedScheme, req.URL.Scheme)
			}
			return nil
		},
	}}
}

// guardedDial is the security boundary: resolve, test, dial the tested IP.
//
// The port is refused before the resolver is even asked, so a fetch of
// http://internal:22/ costs nothing and leaks no DNS query. Every resolved
// address is tested and the first permitted one that connects wins; a
// blocked address is skipped rather than fatal, because a name that answers
// with both a public and a private address is ordinary split-horizon DNS
// and the public half is legitimately fetchable.
func guardedDial(res resolver, isBlocked func(netip.Addr) bool, portOK func(string) bool) func(context.Context, string, string) (net.Conn, error) {
	var dialer net.Dialer

	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("egress: split address %q: %w", address, err)
		}
		if !portOK(port) {
			return nil, fmt.Errorf("%w: %s", ErrBlockedPort, port)
		}

		addrs, err := res.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("egress: resolve %s: %w", host, err)
		}

		lastErr := fmt.Errorf("%w: %s", ErrBlockedAddress, host)
		for _, addr := range addrs {
			// Drop any zone: a zone identifier names a local interface,
			// which is exactly the reachability this guard exists to deny.
			addr = addr.Unmap().WithZone("")
			if isBlocked(addr) {
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// Fetch GETs rawURL through the guard and returns at most limit bytes of the
// body along with the response's declared content type.
//
// The body is truncated at limit rather than refused: a page larger than the
// cap still has a usable <head>, and an image cut short simply fails to
// decode. The caller decides what a truncated body is worth.
func (c *Client) Fetch(ctx context.Context, rawURL string, limit int64) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("egress: parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("%w: %s", ErrBlockedScheme, parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("egress: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	// Identity encoding so the byte cap below counts the bytes that arrive,
	// not the bytes a compressed stream would expand into.
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("egress: get %s: %w", parsed.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("egress: get %s: %w (%d)", parsed.Redacted(), ErrNotOK, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, "", fmt.Errorf("egress: read %s: %w", parsed.Redacted(), err)
	}
	return body, resp.Header.Get("Content-Type"), nil
}
