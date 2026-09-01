package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// TestFreeTextRefusesWhatCannotBeStored covers every field the contract lets
// a client write prose into.
//
// The hole this closes was a 500, not a leak: a message containing a NUL
// passed every check in the handler and failed inside PostgreSQL with
// "invalid byte sequence for encoding UTF8" (SQLSTATE 22021), which the
// handler could only answer as an internal error. Malformed input is the
// caller's mistake and belongs in a 400 — a 500 says the server broke, and
// buries a real fault in noise from anyone who sends one byte of junk.
func TestFreeTextRefusesWhatCannotBeStored(t *testing.T) {
	t.Parallel()

	// JSON escapes, so these are the real runes by the time a handler sees
	// them. \u0000 is the one PostgreSQL refuses outright; the others are
	// terminal control rather than writing.
	unstorable := []struct{ name, escaped string }{
		{"a NUL", `\u0000`},
		{"a bell", `\u0007`},
		{"a backspace", `\u0008`},
		{"an escape", `\u001b`},
		{"a delete", `\u007f`},
	}

	t.Run("message content", func(t *testing.T) {
		t.Parallel()
		for _, tc := range unstorable {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				store := memberStore()
				// Left unwired on purpose: reaching storage at all is the
				// failure, so this fails loudly rather than quietly passing.
				rec := do(t, store, request(http.MethodPost,
					"/api/v1/channels/"+testChannelID+"/messages",
					fmt.Sprintf(`{"client_msg_id":%q,"content":"a%sb"}`, testClientMsgID, tc.escaped),
					withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	t.Run("an edit, not only a send", func(t *testing.T) {
		t.Parallel()
		// The same guard sits on both write paths; testing one and not the
		// other would leave half of it free to be deleted.
		// Wired far enough to reach the content check: the edit path decides
		// channel, then message, then permission, then state, so a store that
		// cannot find the message never gets there. updateMessageContent is
		// left unwired, so a guard that let this through would fail loudly.
		store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
		rec := do(t, store, request(http.MethodPatch,
			"/api/v1/channels/"+testChannelID+"/messages/"+testMessageID,
			`{"content":"fixed\u0000it"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("channel topic", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		rec := do(t, store, request(http.MethodPatch,
			"/api/v1/channels/"+testChannelID,
			`{"topic":"ship\u0000it"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("a new channel's topic", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		rec := do(t, store, request(http.MethodPost, "/api/v1/channels",
			`{"slug":"deploys","kind":"public","topic":"ship\u0000it"}`,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("the search needle", func(t *testing.T) {
		t.Parallel()
		store, _ := searchingStore(storage.SearchPage{})
		rec := do(t, store, request(http.MethodGet,
			"/api/v1/search?q=a%00b", "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("the directory filter", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.listDirectory = func(context.Context, storage.ListDirectoryParams) ([]storage.User, error) {
			t.Error("storage was reached with an unstorable filter")
			return nil, nil
		}
		rec := do(t, store, request(http.MethodGet,
			"/api/v1/users?q=a%00b", "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})
}

// TestFreeTextAcceptsRealWriting is the other half, and the more important
// one: a validator that refuses what people actually type is worse than none.
func TestFreeTextAcceptsRealWriting(t *testing.T) {
	t.Parallel()

	accepted := []struct{ name, content string }{
		{"a multi-line markdown message", "First line.\n\n- a list item\n- another"},
		{"a tab", "before\tafter"},
		// Persian is written with the zero-width non-joiner, and the UI
		// isolates names with bidi controls. Refusing either would break the
		// language this product is half written in.
		//nolint:staticcheck // ST1018: the U+200C is the point of this case — it is required Persian spelling, and escaping it would hide what is being tested.
		{"Persian with a zero-width non-joiner", "می‌رود و می‌آید"},
		{"a bidi isolate around a Latin name", "\u2068Roberto Silva\u2069 گفت"},
		{"an emoji", "shipped 🎉"},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := memberStore()
			var got storage.NewMessage
			store.createMessage = func(_ context.Context, nm storage.NewMessage) (storage.Message, bool, error) {
				got = nm
				return fixtureMessage(), true, nil
			}
			// Encoded rather than interpolated: the point is that these
			// runes survive a real round trip, so the JSON has to be built
			// the way a client builds it.
			encoded, err := json.Marshal(map[string]string{
				"client_msg_id": testClientMsgID,
				"content":       tc.content,
			})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			rec := do(t, store, request(http.MethodPost,
				"/api/v1/channels/"+testChannelID+"/messages", string(encoded),
				withSessionCookie("tok"), withCSRF()))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status %d, want 201 (body %s)", rec.Code, rec.Body.String())
			}
			// Stored as authored: the server validates and never rewrites.
			if got.Content != tc.content {
				t.Errorf("stored %q, want %q unchanged", got.Content, tc.content)
			}
		})
	}
}
