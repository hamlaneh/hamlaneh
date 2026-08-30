package httpserver_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The anti-downgrade boundary, asked from both sides (ADR 006).
//
// Every case below is a write that must NOT be stored, so every one of them
// leaves the store's write methods unwired: an unwired method fails with
// errFakeUnwired, so a refusal that leaked through would surface as a 500
// rather than passing quietly. That is the point of the shape — the assertion
// is not only "the right code came back" but "storage was never reached".

// e2eeCiphertext is a well-formed envelope payload. The server never parses
// one, so its bytes are arbitrary; what matters is that it is valid base64
// within the contract's cap.
var e2eeCiphertext = base64.StdEncoding.EncodeToString([]byte("ciphertext"))

// encryptedChannel is the fixture channel with e2ee on.
func encryptedChannel() storage.Channel {
	ch := fixtureChannel()
	ch.E2EE = true
	return ch
}

// encryptedMemberStore is a member of an encrypted channel.
func encryptedMemberStore() *fakeStore {
	return channelStore(fixtureUser(), encryptedChannel(), true)
}

// mlsEnvelope is the JSON fragment a send or edit carries on an e2ee channel.
func mlsEnvelope(epoch int, ciphertext string) string {
	return fmt.Sprintf(`"mls":{"epoch":%d,"ciphertext":%q}`, epoch, ciphertext)
}

func sendBody(parts ...string) string {
	return "{" + `"client_msg_id":"` + testClientMsgID + `",` + strings.Join(parts, ",") + "}"
}

func TestSendMessageE2EEBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		encrypted bool
		body      string
		wantCode  string
	}{
		{
			// The downgrade itself: plaintext arriving on an encrypted
			// conversation. Nothing else in the server would notice.
			name:      "plaintext send on an encrypted channel",
			encrypted: true,
			body:      sendBody(`"content":"hello"`),
			wantCode:  "e2ee_required",
		},
		{
			name:      "empty send with no envelope on an encrypted channel",
			encrypted: true,
			body:      sendBody(`"content":""`),
			wantCode:  "e2ee_required",
		},
		{
			// Ciphertext AND words. Storing this would put the message in the
			// searchable column, which is the same disclosure as sending it
			// in the clear — so it is the same refusal.
			name:      "content beside the ciphertext on an encrypted channel",
			encrypted: true,
			body:      sendBody(`"content":"hello"`, mlsEnvelope(1, e2eeCiphertext)),
			wantCode:  "e2ee_required",
		},
		{
			name:      "attachments on an encrypted channel",
			encrypted: true,
			body: sendBody(`"content":""`, mlsEnvelope(1, e2eeCiphertext),
				`"attachment_ids":["`+testMessageID+`"]`),
			wantCode: "e2ee_attachments_unsupported",
		},
		{
			// The other direction, and it matters as much: a ciphertext
			// stored on a plaintext channel would make "encrypted" a property
			// of a message rather than of the conversation.
			name:      "ciphertext on a plaintext channel",
			encrypted: false,
			body:      sendBody(`"content":""`, mlsEnvelope(1, e2eeCiphertext)),
			wantCode:  "e2ee_not_enabled",
		},
		{
			name:      "ciphertext beside content on a plaintext channel",
			encrypted: false,
			body:      sendBody(`"content":"hello"`, mlsEnvelope(1, e2eeCiphertext)),
			wantCode:  "e2ee_not_enabled",
		},
		{
			name:      "a negative epoch",
			encrypted: true,
			body:      sendBody(`"content":""`, mlsEnvelope(-1, e2eeCiphertext)),
			wantCode:  "invalid_request",
		},
		{
			name:      "ciphertext that is not base64",
			encrypted: true,
			body:      sendBody(`"content":""`, mlsEnvelope(1, "not base64 at all!")),
			wantCode:  "invalid_request",
		},
		{
			name:      "an empty ciphertext",
			encrypted: true,
			body:      sendBody(`"content":""`, mlsEnvelope(1, "")),
			wantCode:  "invalid_request",
		},
		{
			name:      "a ciphertext over the contract's cap",
			encrypted: true,
			body:      sendBody(`"content":""`, mlsEnvelope(1, strings.Repeat("A", 45004))),
			wantCode:  "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ch := fixtureChannel()
			if tt.encrypted {
				ch = encryptedChannel()
			}
			// createMessage stays unwired on purpose: a refusal that leaked
			// through would be a 500 here, not a quiet pass.
			store := channelStore(fixtureUser(), ch, true)

			rec := do(t, store, request(http.MethodPost, channelPath("/messages"), tt.body,
				withSessionCookie("tok"), withCSRF()))
			wantError(t, rec, http.StatusBadRequest, tt.wantCode)
		})
	}
}

func TestSendMessageEncryptedIsStored(t *testing.T) {
	t.Parallel()

	store := encryptedMemberStore()
	var got storage.NewMessage
	store.createMessage = func(_ context.Context, nm storage.NewMessage) (storage.Message, bool, error) {
		got = nm
		msg := fixtureMessage()
		msg.Content = ""
		msg.Mls = nm.Mls
		return msg, true, nil
	}

	rec := do(t, store, request(http.MethodPost, channelPath("/messages"),
		sendBody(`"content":""`, mlsEnvelope(4, e2eeCiphertext)),
		withSessionCookie("tok"), withCSRF()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if got.Mls == nil || got.Mls.Epoch != 4 || string(got.Mls.Ciphertext) != "ciphertext" {
		t.Fatalf("storage got envelope %+v, want the decoded ciphertext at epoch 4", got.Mls)
	}
	if got.Content != "" {
		t.Errorf("storage got content %q; the searchable column must stay empty", got.Content)
	}
	// And the response echoes it as base64, which is the only encoding the
	// contract has: the decode on the way in and the encode on the way out
	// have to be the same alphabet or a blob does not round-trip.
	if !strings.Contains(rec.Body.String(), e2eeCiphertext) {
		t.Errorf("response %s does not echo the ciphertext", rec.Body.String())
	}
}

func TestEditMessageE2EEBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		encrypted bool
		body      string
		wantCode  string
	}{
		{
			name:      "plaintext edit on an encrypted channel",
			encrypted: true,
			body:      `{"content":"rewritten"}`,
			wantCode:  "e2ee_required",
		},
		{
			name:      "content beside the ciphertext on an encrypted channel",
			encrypted: true,
			body:      `{"content":"rewritten",` + mlsEnvelope(2, e2eeCiphertext) + `}`,
			wantCode:  "e2ee_required",
		},
		{
			name:      "ciphertext on a plaintext channel",
			encrypted: false,
			body:      `{"content":"",` + mlsEnvelope(2, e2eeCiphertext) + `}`,
			wantCode:  "e2ee_not_enabled",
		},
		{
			// The plaintext minimum still applies where it should: an empty
			// edit on a plaintext channel is not suddenly legal because the
			// e2ee path allows one.
			name:      "an empty edit on a plaintext channel",
			encrypted: false,
			body:      `{"content":""}`,
			wantCode:  "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ch := fixtureChannel()
			if tt.encrypted {
				ch = encryptedChannel()
			}
			store := &editStore{
				fakeStore: channelStore(fixtureUser(), ch, true),
				messageByID: func(context.Context, uuid.UUID, uuid.UUID) (storage.Message, error) {
					return fixtureMessage(), nil
				},
				// updateMessageContent stays unwired: reaching it is a 500.
			}

			rec := do(t, store, request(http.MethodPatch, channelPath("/messages/"+testMessageID),
				tt.body, withSessionCookie("tok"), withCSRF()))
			wantError(t, rec, http.StatusBadRequest, tt.wantCode)
		})
	}
}

func TestEditMessageEncryptedReplacesCiphertext(t *testing.T) {
	t.Parallel()

	var gotContent string
	var gotMls *storage.MessageMls
	store := &editStore{
		fakeStore: channelStore(fixtureUser(), encryptedChannel(), true),
		messageByID: func(context.Context, uuid.UUID, uuid.UUID) (storage.Message, error) {
			return fixtureMessage(), nil
		},
		updateMessageContent: func(
			_ context.Context, _, _ uuid.UUID, content string, mls *storage.MessageMls,
		) (storage.Message, error) {
			gotContent, gotMls = content, mls
			msg := fixtureMessage()
			msg.Content = ""
			msg.Mls = mls
			return msg, nil
		},
	}

	rec := do(t, store, request(http.MethodPatch, channelPath("/messages/"+testMessageID),
		`{"content":"",`+mlsEnvelope(9, e2eeCiphertext)+`}`, withSessionCookie("tok"), withCSRF()))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotContent != "" {
		t.Errorf("storage got content %q, want empty", gotContent)
	}
	if gotMls == nil || gotMls.Epoch != 9 {
		t.Errorf("storage got envelope %+v, want the replacement at epoch 9", gotMls)
	}
}

// TestUploadFileRefusedOnEncryptedChannel pins the refusal at the upload
// route rather than only at the send.
//
// Refusing here is what keeps the bandwidth unspent: the alternative is a
// server that accepts twenty-five megabytes and then refuses to let anybody
// attach them. The blob store stays unwired, so a leak past this check is a
// 500 rather than a silently stored file.
func TestUploadFileRefusedOnEncryptedChannel(t *testing.T) {
	t.Parallel()

	rec := do(t, encryptedMemberStore(),
		request(http.MethodPost, channelPath("/files"), "", withSessionCookie("tok"), withCSRF()))
	wantError(t, rec, http.StatusBadRequest, "e2ee_attachments_unsupported")
}

// TestUploadFileRefusalComesAfterMembership pins the ordering: a stranger to
// an encrypted channel learns that it does not exist, never that it is
// encrypted.
func TestUploadFileRefusalComesAfterMembership(t *testing.T) {
	t.Parallel()

	store := channelStore(fixtureUser(), encryptedChannel(), false)
	rec := do(t, store,
		request(http.MethodPost, channelPath("/files"), "", withSessionCookie("tok"), withCSRF()))
	wantError(t, rec, http.StatusNotFound, "channel_not_found")
}

// TestChannelCreationCarriesE2EE pins that a created channel's encryption
// reaches the response, so a client can render what it just made.
//
// Which value it gets is no longer a request bound: the organisation's mode
// decides it (ADR 011), and that rule — both modes, both creation paths,
// omitted, matching and mismatching flags — is pinned in
// orgencryptionmode_test.go. What is left here is the wiring.
func TestChannelCreationCarriesE2EE(t *testing.T) {
	t.Parallel()

	store := authedStore(fixtureUser())
	var got storage.NewChannel
	store.createChannel = func(_ context.Context, nc storage.NewChannel) (storage.Channel, error) {
		got = nc
		return encryptedChannel(), nil
	}

	rec := do(t, store, request(http.MethodPost, "/api/v1/channels",
		`{"slug":"secrets","kind":"private","e2ee":true}`, withSessionCookie("tok"), withCSRF()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if !got.E2EE {
		t.Error("storage got e2ee=false although the instance is strict")
	}
	if !strings.Contains(rec.Body.String(), `"e2ee":true`) {
		t.Errorf("response %s does not report the channel as encrypted", rec.Body.String())
	}
}
