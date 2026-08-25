package wsgateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestOriginAllowed pins the CSWSH defense (§1). The Origin header is the
// only thing standing in for CSRF on a WebSocket handshake, so every one of
// these must stay a refusal: no wildcard, no substring match, no
// registrable-domain relaxation.
func TestOriginAllowed(t *testing.T) {
	t.Parallel()

	const configured = "https://chat.example.com"
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{"exact", "https://chat.example.com", true},
		{"case insensitive scheme and host", "HTTPS://CHAT.EXAMPLE.COM", true},
		{"missing", "", false},
		{"null", "null", false},
		{"plain http", "http://chat.example.com", false},
		{"other host", "https://evil.example.com", false},
		{"subdomain of ours", "https://a.chat.example.com", false},
		{"registrable domain", "https://example.com", false},
		{"prefix", "https://chat.example.com.evil.net", false},
		{"suffix", "https://evil.net/https://chat.example.com", false},
		{"with port", "https://chat.example.com:443", false},
		{"trailing slash", "https://chat.example.com/", false},
	}

	g := New(newFakeStore(), configured)
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := g.OriginAllowed(tc.origin); got != tc.want {
				t.Errorf("OriginAllowed(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}
}

// TestConfiguredOriginIsNormalized: the instance is configured with a URL,
// which may carry a sub-path or a default port; a browser's Origin carries
// neither. A configuration that cannot be read as an origin at all allows
// nothing.
func TestConfiguredOriginIsNormalized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured string
		origin     string
		want       bool
	}{
		{"sub-path install", "https://chat.example.com/hamlaneh", "https://chat.example.com", true},
		{"trailing slash", "https://chat.example.com/", "https://chat.example.com", true},
		{"default https port", "https://chat.example.com:443", "https://chat.example.com", true},
		{"default http port", "http://localhost:80", "http://localhost", true},
		{"explicit non-default port is kept", "https://chat.example.com:8443", "https://chat.example.com:8443", true},
		{"a port is not optional when it was configured", "https://chat.example.com:8443", "https://chat.example.com", false},
		{"host only, no scheme", "chat.example.com", "https://chat.example.com", false},
		{"not a url at all", "://::", "https://chat.example.com", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := New(newFakeStore(), tc.configured)
			t.Cleanup(func() {
				if err := g.Close(); err != nil {
					t.Errorf("close gateway: %v", err)
				}
			})
			if got := g.OriginAllowed(tc.origin); got != tc.want {
				t.Errorf("gateway on %q: OriginAllowed(%q) = %v, want %v",
					tc.configured, tc.origin, got, tc.want)
			}
		})
	}
}

// TestOriginAllowedWithoutConfiguredOrigin proves the fail-closed default: a
// gateway that does not know its own public origin allows nothing, because
// "I cannot tell whether this is same-site" must never read as "yes".
func TestOriginAllowedWithoutConfiguredOrigin(t *testing.T) {
	t.Parallel()

	g := New(newFakeStore(), "")
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})

	for _, origin := range []string{"", "null", "https://chat.example.com", "*"} {
		if g.OriginAllowed(origin) {
			t.Errorf("OriginAllowed(%q) = true on an unconfigured gateway", origin)
		}
	}
}

// TestDisallowedOriginRejectedBeforeUpgrade is (g) end to end: a handshake
// from another site never becomes a socket.
func TestDisallowedOriginRejectedBeforeUpgrade(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	familyID := h.store.addFamily(user.ID)

	header := http.Header{}
	header.Set("Origin", "https://evil.example.com")
	header.Set(testUserHeader, user.ID.String())
	header.Set(testFamilyHeader, familyID.String())

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, h.server.URL+"/api/v1/ws", &websocket.DialOptions{
		HTTPHeader: header,
	})
	if resp != nil && resp.Body != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close handshake response: %v", closeErr)
		}
	}
	if err == nil {
		if closeErr := conn.CloseNow(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
		t.Fatal("a cross-site handshake opened a socket")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 on the handshake, got %v (err %v)", resp, err)
	}
}

func TestHelloOK(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	familyID := h.store.addFamily(user.ID)

	got := h.dial(user, familyID).hello()

	if got.ProtocolVersion != protocolVersion {
		t.Errorf("protocol_version = %d, want %d", got.ProtocolVersion, protocolVersion)
	}
	if got.UserID != user.ID {
		t.Errorf("user_id = %s, want %s", got.UserID, user.ID)
	}
	if got.SessionFamilyID != familyID {
		t.Errorf("session_family_id = %s, want %s", got.SessionFamilyID, familyID)
	}
	if got.MaxFrameBytes != maxFrameBytes {
		t.Errorf("max_frame_bytes = %d, want %d", got.MaxFrameBytes, maxFrameBytes)
	}
	if got.HeartbeatIntervalSeconds != int(heartbeatInterval/time.Second) {
		t.Errorf("heartbeat_interval_seconds = %d, want %d",
			got.HeartbeatIntervalSeconds, int(heartbeatInterval/time.Second))
	}
	if len(got.Resumed) != 0 || len(got.Resync) != 0 {
		t.Errorf("a fresh connect resumed %v and resynced %v", got.Resumed, got.Resync)
	}
}

func TestFirstFrameMustBeHello(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))

	c.send(map[string]any{"type": typePing, "id": "1"})

	if code := c.waitClose(); code != closeProtocolError {
		t.Fatalf("close code = %d, want %d", code, closeProtocolError)
	}
}

func TestSilentSocketIsClosed(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithHelloTimeout(100*time.Millisecond))
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))

	if code := c.waitClose(); code != closeProtocolError {
		t.Fatalf("close code = %d, want %d", code, closeProtocolError)
	}
}

func TestUnsupportedProtocolVersion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))

	c.send(map[string]any{
		"type": typeHello, "id": "1",
		"data": map[string]any{"protocol_version": protocolVersion + 1},
	})

	if code := c.waitClose(); code != closeProtocolError {
		t.Fatalf("close code = %d, want %d", code, closeProtocolError)
	}
	var closeErr websocket.CloseError
	if !errors.As(c.readErr, &closeErr) {
		t.Fatalf("no close error recorded: %v", c.readErr)
	}
	if closeErr.Reason != "unsupported_protocol_version" {
		t.Errorf("close reason = %q, want unsupported_protocol_version", closeErr.Reason)
	}
}

func TestSecondHelloIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))
	c.hello()

	c.send(map[string]any{
		"type": typeHello, "id": "2",
		"data": map[string]any{"protocol_version": protocolVersion},
	})

	if code := c.waitClose(); code != closeProtocolError {
		t.Fatalf("close code = %d, want %d", code, closeProtocolError)
	}
}

// TestMalformedFramesAreFatal walks the §2 list. There is no partial-parse
// recovery: an ambiguous frame is a bug or an attack, and neither deserves a
// best-effort interpretation.
func TestMalformedFramesAreFatal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{"invalid json", `{"type":`},
		{"not an object", `"hello"`},
		{"missing type", `{"ts":"2026-08-21T09:12:00Z","data":{}}`},
		{"missing ts", `{"type":"ping","data":{}}`},
		{"data is null", `{"type":"ping","ts":"2026-08-21T09:12:00Z","data":null}`},
		{"data is a scalar", `{"type":"ping","ts":"2026-08-21T09:12:00Z","data":7}`},
		{"data is an array", `{"type":"ping","ts":"2026-08-21T09:12:00Z","data":[]}`},
		{"data missing", `{"type":"ping","ts":"2026-08-21T09:12:00Z"}`},
		{"subscribe without chan", `{"type":"subscribe","ts":"2026-08-21T09:12:00Z","data":{}}`},
		{"typing without chan", `{"type":"typing","ts":"2026-08-21T09:12:00Z","data":{}}`},
		{"id too long", `{"type":"ping","id":"` + strings.Repeat("x", 65) +
			`","ts":"2026-08-21T09:12:00Z","data":{}}`},
		{"ts is not a time", `{"type":"ping","ts":"yesterday","data":{}}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			user := h.store.addUser("alice")
			c := h.dial(user, h.store.addFamily(user.ID))
			c.hello()

			c.sendRaw([]byte(tc.raw))

			if code := c.waitClose(); code != closeProtocolError {
				t.Fatalf("close code = %d, want %d", code, closeProtocolError)
			}
		})
	}
}

func TestBinaryFrameIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))
	c.hello()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write binary frame: %v", err)
	}

	if code := c.waitClose(); code != closeProtocolError {
		t.Fatalf("close code = %d, want %d", code, closeProtocolError)
	}
}

// TestFrameSizeCap is §2's 64 KiB bound: a larger frame closes the socket
// with 4413, and the server does not read the rest of it.
func TestFrameSizeCap(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))
	c.hello()

	oversize := `{"type":"ping","ts":"2026-08-21T09:12:00Z","data":{"pad":"` +
		strings.Repeat("x", maxFrameBytes) + `"}}`
	if len(oversize) <= maxFrameBytes {
		t.Fatalf("test frame is only %d bytes", len(oversize))
	}
	c.sendRaw([]byte(oversize))

	if code := c.waitClose(); code != closeFrameTooLarge {
		t.Fatalf("close code = %d, want %d", code, closeFrameTooLarge)
	}
}

// TestFrameAtTheCapIsAccepted is the other half: the boundary itself is
// legal, so a cap that is off by one would be caught here.
func TestFrameAtTheCapIsAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))
	c.hello()

	frame := `{"type":"ping","id":"1","ts":"2026-08-21T09:12:00Z","data":{"pad":"%s"}}`
	pad := maxFrameBytes - (len(frame) - 2)
	c.sendRaw([]byte(strings.Replace(frame, "%s", strings.Repeat("x", pad), 1)))

	if got := c.expect(typePong); got["id"] != "1" {
		t.Errorf("pong echoed id %v, want 1", got["id"])
	}
}

// TestUnknownTypeIsIgnored is what lets an old client survive a newer server
// (§2): the frame is dropped and the socket stays open.
func TestUnknownTypeIsIgnored(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))
	c.hello()

	c.send(map[string]any{"type": "reaction_added_in_phase_5", "id": "x",
		"data": map[string]any{"unknown_field": true}})
	c.send(map[string]any{"type": typePing, "id": "1"})

	if got := c.expect(typePong); got["id"] != "1" {
		t.Errorf("pong echoed id %v, want 1", got["id"])
	}
}

func TestHeartbeatTimeout(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithHeartbeat(30*time.Millisecond, 100*time.Millisecond))
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))
	c.hello()

	// Say nothing at all. The server pings, gets no answer, and closes.
	if code := c.waitClose(); code != closeHeartbeat {
		t.Fatalf("close code = %d, want %d", code, closeHeartbeat)
	}
}

// TestHeartbeatKeepsQuietSocketAlive is the inverse: a client that answers
// the pings is not disconnected.
func TestHeartbeatKeepsQuietSocketAlive(t *testing.T) {
	t.Parallel()

	// Two numbers matter here and they pull against each other. The timeout
	// has to be long enough that ordinary scheduling delay is not mistaken
	// for a dead client — under -race with the whole module running, the test
	// goroutine can be starved for a long time, and at 100ms that starvation
	// was failing this test for a reason that was not the code's. It also has
	// to be short enough that the loop below outlives it, or a server that
	// ignored pongs entirely would never get the chance to disconnect and
	// this test would assert nothing. So: one second of tolerance, answered
	// for twice that long.
	//
	// Checked by mutation — stopping inbound frames from refreshing liveness
	// must fail this test, and at a five-second timeout it did not.
	const heartbeatTimeout = time.Second

	h := newHarness(t, WithHeartbeat(30*time.Millisecond, heartbeatTimeout))
	user := h.store.addUser("alice")
	c := h.dial(user, h.store.addFamily(user.ID))
	c.hello()

	deadline := time.Now().Add(2 * heartbeatTimeout)
	for time.Now().Before(deadline) {
		c.expect(typePing)
		c.send(map[string]any{"type": typePong})
	}

	c.send(map[string]any{"type": typePing, "id": "alive"})
	if got := c.expect(typePong); got["id"] != "alive" {
		t.Errorf("pong echoed id %v, want alive", got["id"])
	}
}
