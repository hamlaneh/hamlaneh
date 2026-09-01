package wsgateway_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/wsgateway"
)

// These tests drive the real HTTP stack — the generated router, the security
// middleware, the security headers and httpserver.ConnectWebSocket — because
// three of the properties the slice has to prove live above the gateway: a
// handshake with no session, a handshake with a dead one, and a handshake
// from another origin. They also prove the upgrade survives the wrapping,
// which no unit test can.

// stackStore is the storage the stack needs. Everything the tests do not
// exercise is left to the embedded nil interface, so an unexpected call
// fails loudly rather than quietly returning a zero value.
type stackStore struct {
	httpserver.Store

	sessions map[string]sessionUser
}

type sessionUser struct {
	session storage.Session
	user    storage.User
}

func (s *stackStore) SessionUserByAccessHash(_ context.Context, hash []byte) (storage.Session, storage.User, error) {
	su, ok := s.sessions[string(hash)]
	if !ok {
		return storage.Session{}, storage.User{}, storage.ErrNotFound
	}
	return su.session, su.user, nil
}

// The five reads the gateway makes. Nothing in these tests has a channel, so
// they answer honestly and emptily.

func (s *stackStore) IsChannelMember(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (s *stackStore) ChannelForUser(context.Context, uuid.UUID, uuid.UUID) (storage.Channel, error) {
	return storage.Channel{}, storage.ErrChannelNotFound
}

func (s *stackStore) ListChannelMembers(context.Context, uuid.UUID, storage.ListChannelMembersParams) ([]storage.User, error) {
	return nil, nil
}

func (s *stackStore) ListChannelsForUser(context.Context, uuid.UUID, storage.ListChannelsParams) ([]storage.Channel, error) {
	return nil, nil
}

func (s *stackStore) ListSessionFamilies(_ context.Context, userID, _ uuid.UUID) ([]storage.SessionFamily, error) {
	out := []storage.SessionFamily{}
	for _, su := range s.sessions {
		if su.user.ID == userID {
			out = append(out, storage.SessionFamily{FamilyID: su.session.FamilyID})
		}
	}
	return out, nil
}

// requestDeadline is the read and write timeout the test server applies to
// an ordinary request, standing in for the production values.
const requestDeadline = 700 * time.Millisecond

type stack struct {
	server *httptest.Server
	store  *stackStore
	gw     *wsgateway.Gateway
	origin string
	token  string
}

func newStack(t *testing.T) *stack {
	t.Helper()

	user := storage.User{ID: uuid.New(), Username: "alice", DisplayName: "Alice"}
	token, hash := session.NewToken()
	store := &stackStore{sessions: map[string]sessionUser{
		string(hash): {
			session: storage.Session{ID: uuid.New(), UserID: user.ID, FamilyID: uuid.New()},
			user:    user,
		},
	}}

	// Unstarted, so the gateway can be told the origin the server is about to
	// serve on before anything can reach it.
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()

	gw := wsgateway.New(store, origin)
	// WithTrustedProxy because this stack stands in for the compose
	// deployment, where Caddy terminates every connection — it is what makes
	// the forwarded-address test below exercise the real derivation rather
	// than a shape the single binary never has.
	server.Config.Handler = httpserver.Handler(store,
		httpserver.WithRealtime(gw), httpserver.WithTrustedProxy(true))
	// The production server sets request deadlines (httpserver.New: 10s read,
	// 30s write). Short ones here let one test prove a socket outlives them,
	// which is the difference between a realtime connection and one that
	// dies a few seconds in for reasons nobody would find.
	server.Config.ReadTimeout = requestDeadline
	server.Config.WriteTimeout = requestDeadline
	server.Start()

	t.Cleanup(func() {
		if err := gw.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
		server.Close()
	})
	return &stack{server: server, store: store, gw: gw, origin: origin, token: token}
}

// upgrade attempts the handshake and returns the response for the cases that
// never become a socket.
func (s *stack) upgrade(t *testing.T, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, s.server.URL+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: header})
}

func (s *stack) header(origin, token string) http.Header {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	if token != "" {
		h.Set("Cookie", session.AccessCookie+"="+token)
	}
	return h
}

// TestHandshakeWithoutASessionIsRejected is (a).
func TestHandshakeWithoutASessionIsRejected(t *testing.T) {
	t.Parallel()

	s := newStack(t)
	conn, resp, err := s.upgrade(t, s.header(s.origin, ""))
	assertRefused(t, conn, resp, err, http.StatusUnauthorized, "not_authenticated")
}

// TestHandshakeWithARevokedSessionIsRejected is (b). A revoked, expired or
// simply unknown access token is one lookup that finds nothing — the same
// answer for all three, which is what keeps a guessed token from confirming
// anything.
func TestHandshakeWithARevokedSessionIsRejected(t *testing.T) {
	t.Parallel()

	s := newStack(t)

	// Revocation, from the handshake's point of view: the token no longer
	// resolves to a live session.
	clear(s.store.sessions)

	conn, resp, err := s.upgrade(t, s.header(s.origin, s.token))
	assertRefused(t, conn, resp, err, http.StatusUnauthorized, "not_authenticated")
}

// TestHandshakeFromAnotherOriginIsRejected is (g) through the real endpoint:
// a valid session is necessary and not sufficient to open a socket.
func TestHandshakeFromAnotherOriginIsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		origin string
	}{
		{"missing", ""},
		{"null", "null"},
		{"another site", "https://evil.example.com"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newStack(t)
			conn, resp, err := s.upgrade(t, s.header(tc.origin, s.token))
			assertRefused(t, conn, resp, err, http.StatusForbidden, "origin_not_allowed")
		})
	}
}

// TestHandshakeSucceedsThroughTheWholeStack proves the upgrade survives the
// security headers, the generated router and the security middleware, and
// that the socket outlives the HTTP server's request deadlines.
func TestHandshakeSucceedsThroughTheWholeStack(t *testing.T) {
	t.Parallel()

	s := newStack(t)
	conn, resp, err := s.upgrade(t, s.header(s.origin, s.token))
	if resp != nil && resp.Body != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close handshake response: %v", closeErr)
		}
	}
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hello := `{"type":"hello","id":"1","ts":"2026-08-21T09:12:00Z","data":{"protocol_version":1}}`
	if writeErr := conn.Write(ctx, websocket.MessageText, []byte(hello)); writeErr != nil {
		t.Fatalf("write hello: %v", writeErr)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read hello_ok: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"hello_ok"`) {
		t.Fatalf("first server frame = %s, want hello_ok", raw)
	}

	// Past the request deadlines the HTTP server applied to the handshake. A
	// socket still carrying them is dead by now.
	time.Sleep(requestDeadline + 200*time.Millisecond)

	if writeErr := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"ping","id":"2","ts":"2026-08-21T09:12:00Z","data":{}}`)); writeErr != nil {
		t.Fatalf("write ping: %v", writeErr)
	}
	if _, raw, err = conn.Read(ctx); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"pong"`) {
		t.Fatalf("second server frame = %s, want pong", raw)
	}
}

// maxBurn bounds the loop below. It is far above either connect limit and
// exists so a budget that never refuses fails the test instead of hanging.
const maxBurn = 10_000

// burnConnectBudget fills one address's whole connect budget, taking a fresh
// session family for every connect so the per-family half never bites first.
// That is one address with many signed-in devices behind it — the shape the
// address half of the budget exists for.
func burnConnectBudget(t *testing.T, gw *wsgateway.Gateway, ip string) {
	t.Helper()

	for range maxBurn {
		if _, allowed := gw.ConnectAllowed(uuid.New(), ip); !allowed {
			return
		}
	}
	t.Fatalf("the connect budget admitted %d connects from %s without refusing one", maxBurn, ip)
}

// TestHandshakeOverTheConnectBudgetIsRefusedWithRetryAfter is the §8 connect
// flood, refused where §8 says it is refused: an HTTP 429 on the handshake
// response, before any socket exists, so there is no close code involved.
//
// The Retry-After matters as much as the status. Every 429 this server
// answers carries one, because they all go through one writer — and a
// handshake refusal that skipped it would leave a client guessing at exactly
// the moment it is already backing off blind.
func TestHandshakeOverTheConnectBudgetIsRefusedWithRetryAfter(t *testing.T) {
	t.Parallel()

	s := newStack(t)
	// The test client dials over loopback and sends no X-Forwarded-For, so
	// the address the budget keys on is the peer's own.
	burnConnectBudget(t, s.gw, "127.0.0.1")

	conn, resp, err := s.upgrade(t, s.header(s.origin, s.token))
	assertRefused(t, conn, resp, err, http.StatusTooManyRequests, "rate_limited")

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("the handshake's 429 carried no Retry-After header")
	}
	seconds, convErr := strconv.Atoi(retryAfter)
	if convErr != nil {
		t.Fatalf("Retry-After = %q, want whole seconds: %v", retryAfter, convErr)
	}
	if seconds < 1 {
		t.Fatalf("Retry-After = %d, which invites an immediate retry of what was just refused", seconds)
	}
}

// TestConnectBudgetKeysOnTheForwardedClientAddress pins the derivation, not a
// copy of it: the budget must identify the client the same way every other
// per-address budget in this server does (httpserver.clientIP), which means
// trusting the leftmost X-Forwarded-For value when — and only when — the
// server was told a proxy is in front of it AND the direct peer sits where
// that proxy sits (httpserver.WithTrustedProxy, set by newStack above).
//
// Getting this wrong is not a cosmetic bug in either direction. Keying on the
// peer would put the whole instance behind Caddy into one bucket and let any
// one client flood every colleague out of a socket. Trusting a header from an
// untrusted peer would let a flooder change addresses at will.
func TestConnectBudgetKeysOnTheForwardedClientAddress(t *testing.T) {
	t.Parallel()

	const flooder = "198.51.100.9"

	s := newStack(t)
	burnConnectBudget(t, s.gw, flooder)

	forwarded := func(ip string) http.Header {
		h := s.header(s.origin, s.token)
		h.Set("X-Forwarded-For", ip)
		return h
	}

	t.Run("the flooding client is recognised through the proxy", func(t *testing.T) {
		conn, resp, err := s.upgrade(t, forwarded(flooder))
		assertRefused(t, conn, resp, err, http.StatusTooManyRequests, "rate_limited")
	})

	t.Run("a colleague behind the same proxy still connects", func(t *testing.T) {
		conn, resp, err := s.upgrade(t, forwarded("203.0.113.4"))
		if resp != nil && resp.Body != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Errorf("close handshake response: %v", closeErr)
			}
		}
		if err != nil {
			t.Fatalf("a second client behind the same proxy was refused: %v", err)
		}
		if closeErr := conn.CloseNow(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Errorf("close: %v", closeErr)
		}
	})

	t.Run("the origin check still answers first", func(t *testing.T) {
		// A cross-site handshake is refused for what is wrong with it, and a
		// budget the request never reached cannot be spent by one.
		conn, resp, err := s.upgrade(t, func() http.Header {
			h := forwarded(flooder)
			h.Set("Origin", "https://evil.example.com")
			return h
		}())
		assertRefused(t, conn, resp, err, http.StatusForbidden, "origin_not_allowed")
	})
}

func assertRefused(t *testing.T, conn *websocket.Conn, resp *http.Response, err error, status int, code string) {
	t.Helper()

	if err == nil {
		if closeErr := conn.CloseNow(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Errorf("close: %v", closeErr)
		}
		t.Fatal("the handshake opened a socket that must have been refused")
	}
	if resp == nil {
		t.Fatalf("no handshake response: %v", err)
	}
	if resp.StatusCode != status {
		t.Fatalf("handshake status = %d, want %d", resp.StatusCode, status)
	}

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Errorf("close body: %v", closeErr)
	}
	if !strings.Contains(string(body), `"code":"`+code+`"`) {
		t.Fatalf("handshake body = %s, want the contract Error with code %s", body, code)
	}
}
