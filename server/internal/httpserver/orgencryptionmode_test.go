package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The organisation encryption mode (ADR 011): what conversations are born as,
// and the one endpoint that moves it.

// modeStore is an authenticated member on an instance in the given mode.
func modeStore(mode string) *fakeStore {
	store := authedStore(fixtureUser())
	store.encryptionMode = func(context.Context) (string, error) { return mode, nil }
	return store
}

// TestConversationBirthFollowsTheOrgMode is the creation rule in full: both
// modes, both creation paths, and all three things a request can say about
// the flag.
//
// The compliance rows are exercised here even though the API cannot select
// compliance yet (ADR 011 decision 3). The write path has to be right the day
// the gate lifts, and a branch nothing runs until then is a branch nobody has
// ever seen work.
func TestConversationBirthFollowsTheOrgMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mode       string
		asserted   string // the e2ee fragment a request carries, "" for omitted
		wantE2EE   bool
		wantStatus int
		wantCode   string
	}{
		{
			name: "strict, omitted", mode: storage.EncryptionModeStrict,
			wantE2EE: true, wantStatus: http.StatusCreated,
		},
		{
			name: "strict, asked for encryption", mode: storage.EncryptionModeStrict,
			asserted: `,"e2ee":true`, wantE2EE: true, wantStatus: http.StatusCreated,
		},
		{
			name: "strict, asked for plaintext", mode: storage.EncryptionModeStrict,
			asserted: `,"e2ee":false`, wantStatus: http.StatusBadRequest,
			wantCode: "e2ee_required_by_org",
		},
		{
			name: "compliance, omitted", mode: storage.EncryptionModeCompliance,
			wantStatus: http.StatusCreated,
		},
		{
			name: "compliance, asked for plaintext", mode: storage.EncryptionModeCompliance,
			asserted: `,"e2ee":false`, wantStatus: http.StatusCreated,
		},
		{
			name: "compliance, asked for encryption", mode: storage.EncryptionModeCompliance,
			asserted: `,"e2ee":true`, wantStatus: http.StatusBadRequest,
			wantCode: "e2ee_forbidden_by_org",
		},
		{
			// Anything that is not literally compliance encrypts: an
			// unreadable mode must fail towards encryption, never away.
			name: "an unrecognised mode is treated as strict", mode: "",
			asserted: `,"e2ee":false`, wantStatus: http.StatusBadRequest,
			wantCode: "e2ee_required_by_org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("channel", func(t *testing.T) {
				t.Parallel()
				store := modeStore(tt.mode)
				reached := false
				var got storage.NewChannel
				store.createChannel = func(_ context.Context, nc storage.NewChannel) (storage.Channel, error) {
					reached, got = true, nc
					ch := fixtureChannel()
					ch.E2EE = nc.E2EE
					return ch, nil
				}

				rec := do(t, store, request(http.MethodPost, "/api/v1/channels",
					`{"slug":"born","kind":"private"`+tt.asserted+`}`,
					withSessionCookie("tok"), withCSRF()))

				if tt.wantCode != "" {
					wantError(t, rec, tt.wantStatus, tt.wantCode)
					if reached {
						t.Error("a refused creation still reached storage")
					}
					return
				}
				if rec.Code != tt.wantStatus {
					t.Fatalf("got status %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
				}
				if got.E2EE != tt.wantE2EE {
					t.Errorf("storage got e2ee=%v, want %v — the mode decides", got.E2EE, tt.wantE2EE)
				}
			})

			t.Run("direct message", func(t *testing.T) {
				t.Parallel()
				store := modeStore(tt.mode)
				reached := false
				var gotE2EE bool
				store.openDirectMessage = func(_ context.Context, _, _ uuid.UUID, e2ee bool) (storage.Channel, bool, error) {
					reached, gotE2EE = true, e2ee
					dm := fixtureDM()
					dm.E2EE = e2ee
					return dm, true, nil
				}
				store.channelForUser = dmPerParticipant()

				rec := do(t, store, request(http.MethodPost, "/api/v1/dms",
					`{"user_id":"`+testPeerID+`"`+tt.asserted+`}`,
					withSessionCookie("tok"), withCSRF()))

				if tt.wantCode != "" {
					wantError(t, rec, tt.wantStatus, tt.wantCode)
					if reached {
						t.Error("a refused creation still reached storage")
					}
					return
				}
				if rec.Code != tt.wantStatus {
					t.Fatalf("got status %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
				}
				if gotE2EE != tt.wantE2EE {
					t.Errorf("storage got e2ee=%v, want %v — the mode decides", gotE2EE, tt.wantE2EE)
				}
			})
		})
	}
}

// TestDirectMessageReopenReturnsTheConversationUnchanged is the other half of
// the mode's boundary: it governs births, and a reopen is not one.
//
// A plaintext direct message on a strict instance is exactly the case ADR 011
// decision 2 leaves alone — untouched, usable, and counted rather than
// converted — and reopening it must hand back what is stored, not what the
// mode would create today.
func TestDirectMessageReopenReturnsTheConversationUnchanged(t *testing.T) {
	t.Parallel()

	store := modeStore(storage.EncryptionModeStrict)
	existing := fixtureDM() // stored plaintext, from before the mode existed
	store.openDirectMessage = func(_ context.Context, _, _ uuid.UUID, e2ee bool) (storage.Channel, bool, error) {
		if !e2ee {
			t.Errorf("a strict instance asked storage to create a plaintext direct message")
		}
		return existing, false, nil
	}

	rec := do(t, store, request(http.MethodPost, "/api/v1/dms",
		`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got api.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the contract Channel shape: %v", err)
	}
	if got.E2ee {
		t.Error("reopening returned the conversation as encrypted; the mode may not re-decide one that exists")
	}
}

// TestChannelCreationValidatesBeforeAskingTheMode pins the ordering: a
// malformed request is refused on its own terms, without a database round
// trip for a mode nothing will use.
func TestChannelCreationValidatesBeforeAskingTheMode(t *testing.T) {
	t.Parallel()

	store := authedStore(fixtureUser())
	store.encryptionMode = func(context.Context) (string, error) {
		t.Error("the mode was read for a request that could never be created")
		return storage.EncryptionModeStrict, nil
	}

	rec := do(t, store, request(http.MethodPost, "/api/v1/channels",
		`{"slug":"NO","kind":"public"}`, withSessionCookie("tok"), withCSRF()))
	wantError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestSetOrgEncryptionMode(t *testing.T) {
	t.Parallel()

	t.Run("strict is written and answered with the settings", func(t *testing.T) {
		t.Parallel()

		store := adminStore()
		var got string
		store.setEncryptionMode = func(_ context.Context, mode string) (storage.OrgSettings, error) {
			got = mode
			return storage.OrgSettings{
				OrgName: "Nest", DefaultLocale: "en", RegistrationMode: "invite",
				SessionLifetimeHours: 720, EncryptionMode: mode,
				ConversationsOutsideMode: 4,
			}, nil
		}

		rec := do(t, store, adminAPI(http.MethodPut, "/api/v1/admin/org/encryption-mode",
			`{"encryption_mode":"strict"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if got != storage.EncryptionModeStrict {
			t.Errorf("storage was asked for %q, want strict", got)
		}
		var settings api.OrgSettings
		if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
			t.Fatalf("body is not the contract OrgSettings shape: %v", err)
		}
		if settings.EncryptionMode != api.Strict {
			t.Errorf("encryption_mode = %q, want strict", settings.EncryptionMode)
		}
		// The dialog and the standing note on the settings screen both read
		// this number; an omitted count would read as "the mode covers
		// everything", which is the claim it exists to refuse.
		if settings.ConversationsOutsideMode == nil || *settings.ConversationsOutsideMode != 4 {
			t.Errorf("conversations_outside_mode = %v, want 4", settings.ConversationsOutsideMode)
		}
	})

	t.Run("compliance is refused while it has no substance", func(t *testing.T) {
		t.Parallel()

		store := adminStore()
		store.setEncryptionMode = func(context.Context, string) (storage.OrgSettings, error) {
			t.Error("compliance reached storage; it is not selectable until retention, export and encryption at rest exist")
			return storage.OrgSettings{}, nil
		}

		rec := do(t, store, adminAPI(http.MethodPut, "/api/v1/admin/org/encryption-mode",
			`{"encryption_mode":"compliance"}`))
		wantError(t, rec, http.StatusConflict, "encryption_mode_locked")
	})

	t.Run("rejects anything that is not a mode", func(t *testing.T) {
		t.Parallel()

		for _, body := range []string{`{}`, `{"encryption_mode":"none"}`, `{"encryption_mode":"Strict"}`, `{`} {
			t.Run(body, func(t *testing.T) {
				t.Parallel()
				// setEncryptionMode stays unwired: a rejected request must
				// never reach storage.
				rec := do(t, adminStore(), adminAPI(http.MethodPut,
					"/api/v1/admin/org/encryption-mode", body))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	t.Run("the change is audited", func(t *testing.T) {
		t.Parallel()

		store := adminStore()
		store.setEncryptionMode = func(_ context.Context, mode string) (storage.OrgSettings, error) {
			return storage.OrgSettings{
				OrgName: "Nest", DefaultLocale: "en", RegistrationMode: "invite",
				SessionLifetimeHours: 720, EncryptionMode: mode,
			}, nil
		}
		rec := recordingAudit{}
		handler := httpserver.Handler(store, httpserver.WithAudit(&rec))
		handler.ServeHTTP(newRecorder(), adminAPI(http.MethodPut,
			"/api/v1/admin/org/encryption-mode", `{"encryption_mode":"strict"}`))

		events := rec.snapshot()
		if len(events) != 1 || events[0].Action != "org.encryption_mode_changed" {
			t.Fatalf("recorded %v, want one org.encryption_mode_changed", rec.actions())
		}
		if events[0].ActorID != adminID {
			t.Errorf("recorded actor %s, want the acting admin %s", events[0].ActorID, adminID)
		}
		if events[0].Detail["encryption_mode"] != storage.EncryptionModeStrict {
			t.Errorf("recorded detail %v, want the mode that was written", events[0].Detail)
		}
	})
}
