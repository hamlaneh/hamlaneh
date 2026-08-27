// Package callstest is a fake media server for the tests of everything that
// talks to one: internal/calls itself, and the handlers in
// internal/httpserver that reach LiveKit through it.
//
// It answers RoomService over Twirp's PROTOBUF transport, which is the
// transport production uses — so a test against it exercises the real client,
// generated stubs and all, rather than a shape somebody agreed with by hand.
// It also mints the signature LiveKit puts on a webhook, because a receiver
// that only ever sees valid bodies proves nothing about the refusals.
//
// It lives in its own package rather than in a _test.go file so both callers
// share one fake; two fakes that agree today are how a client and a handler
// end up testing different servers.
package callstest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

// Server is a fake LiveKit RoomService.
//
// The canned answers are plain fields, set before the call under test; the
// recorded ones are read through methods, because the ejection hooks run in
// the background on purpose.
type Server struct {
	// URL is the base address to build a calls.Service against.
	URL string

	// Participants is what ListParticipants answers with.
	Participants []*livekit.ParticipantInfo
	// Rooms is the room names ListRooms answers with.
	Rooms []string
	// NotFound makes ListParticipants answer as it does for a room that does
	// not exist — the ordinary case, since rooms live only as long as the
	// calls in them.
	NotFound bool
	// RemoveFails makes every RemoveParticipant fail, for the retry policy.
	RemoveFails bool

	mu       sync.Mutex
	attempts int
	removals chan *livekit.RoomParticipantIdentity
}

// New starts a fake media server that is stopped when the test ends.
func New(t *testing.T) *Server {
	t.Helper()

	f := &Server{removals: make(chan *livekit.RoomParticipantIdentity, 16)}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.URL = server.URL
	return f
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		// Every RoomService call carries an admin token minted for that one
		// call. A missing one is a bug worth failing on rather than serving.
		twirpError(w, http.StatusUnauthorized, "unauthenticated", "no admin token")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		twirpError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	switch {
	case strings.HasSuffix(r.URL.Path, "/ListParticipants"):
		if f.NotFound {
			twirpError(w, http.StatusNotFound, "not_found", "requested room does not exist")
			return
		}
		writeProto(w, &livekit.ListParticipantsResponse{Participants: f.Participants})
	case strings.HasSuffix(r.URL.Path, "/ListRooms"):
		rooms := make([]*livekit.Room, 0, len(f.Rooms))
		for _, name := range f.Rooms {
			rooms = append(rooms, &livekit.Room{Name: name})
		}
		writeProto(w, &livekit.ListRoomsResponse{Rooms: rooms})
	case strings.HasSuffix(r.URL.Path, "/RemoveParticipant"):
		var req livekit.RoomParticipantIdentity
		if err := proto.Unmarshal(body, &req); err != nil {
			twirpError(w, http.StatusBadRequest, "malformed", err.Error())
			return
		}
		f.mu.Lock()
		f.attempts++
		f.mu.Unlock()
		if f.RemoveFails {
			twirpError(w, http.StatusInternalServerError, "internal", "media server is unwell")
			return
		}
		f.removals <- &req
		writeProto(w, &livekit.RemoveParticipantResponse{})
	default:
		twirpError(w, http.StatusNotFound, "bad_route", r.URL.Path)
	}
}

// AwaitRemoval waits for one RemoveParticipant call and fails the test if
// none arrives. Both removal hooks eject in the background — the request that
// triggered them has already committed — so a test waits rather than assumes.
func (f *Server) AwaitRemoval(t *testing.T) *livekit.RoomParticipantIdentity {
	t.Helper()

	select {
	case req := <-f.removals:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("no participant was removed")
		return nil
	}
}

// NoRemoval asserts that nothing was ejected. It waits a moment first,
// because the thing it is asserting the absence of is asynchronous.
func (f *Server) NoRemoval(t *testing.T) {
	t.Helper()

	select {
	case req := <-f.removals:
		t.Fatalf("removed %s from %s; nothing should have been ejected",
			req.GetIdentity(), req.GetRoom())
	case <-time.After(250 * time.Millisecond):
	}
}

// RemoveAttempts is how many times RemoveParticipant was called, failures
// included. It is what pins "retried once, then logged".
func (f *Server) RemoveAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// SignedWebhook builds the request LiveKit's notifier sends: the body, plus
// an Authorization token issued by an API key whose sha256 claim is the
// digest of those exact bytes.
func SignedWebhook(t *testing.T, key, secret, body string) *http.Request {
	t.Helper()

	sum := sha256.Sum256([]byte(body))
	token, err := auth.NewAccessToken(key, secret).
		SetValidFor(time.Minute).
		SetSha256(base64.StdEncoding.EncodeToString(sum[:])).
		ToJWT()
	if err != nil {
		t.Fatalf("sign webhook: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/livekit/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/webhook+json")
	req.Header.Set("Authorization", token)
	return req
}

func writeProto(w http.ResponseWriter, msg proto.Message) {
	data, err := proto.Marshal(msg)
	if err != nil {
		twirpError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/protobuf")
	_, _ = w.Write(data)
}

func twirpError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "msg": msg})
}
