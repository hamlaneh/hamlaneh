package httpserver

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The anti-downgrade boundary: the one place a message write is checked
// against its channel's mode.
//
// It is enforced in BOTH directions, and that is the whole point. An e2ee
// channel refusing plaintext is the obvious half — without it, anything that
// can talk a client into omitting the envelope has silently turned encryption
// off for that conversation. A plaintext channel refusing ciphertext is the
// less obvious half and matters as much: it keeps "encrypted" a property of
// the channel rather than of whichever message happened to carry an envelope,
// so there is no per-message ambiguity for a client to render wrongly and no
// state a server could flip.
//
// Both halves live in one function, called by the send path and the edit
// path, because two copies of a mode check are two chances for them to
// disagree about what an e2ee channel accepts — and the disagreement would
// show up as exactly one write path that takes plaintext.
//
// A channel's mode cannot change under this check. e2ee is fixed at creation
// with no update path in the contract, in storage, or in this package, so the
// flag read alongside the channel is the flag the message will be stored
// under.

// messageBody is the half of a send or an edit that the boundary decides on.
//
// Attachments are deliberately absent. They used to be refused here; ADR 013
// legalized them, and there is nothing left for this check to decide about
// them — an id names opaque bytes whose key and real name are inside the
// ciphertext, so a file in an encrypted conversation is as encrypted as the
// words beside it. The claim of each id stays where it always was: inside
// the send's own transaction, against the channel the row was born in.
type messageBody struct {
	content string
	mls     *api.MlsMessageEnvelope
}

// e2eeBody enforces the channel's mode on one message write and returns what
// storage should keep: the envelope on an e2ee channel, nil on a plaintext
// one. On a violation it answers 400 with the contract's code and reports
// false.
//
// Two refusals, two codes, because they ask for two different things from a
// client:
//
//   - e2ee_required — this conversation is encrypted and this write is not.
//     It covers a missing envelope AND non-empty content, which are the same
//     mistake seen from two sides: the searchable column must hold nothing
//     the server can read, so plaintext arriving beside a ciphertext is as
//     much a downgrade as plaintext arriving alone.
//   - e2ee_not_enabled — this conversation is not encrypted and this write
//     is. Storing the ciphertext would be the mode ambiguity above.
func (s *apiServer) e2eeBody(w http.ResponseWriter, r *http.Request, e2ee bool, body messageBody) (*storage.MessageMls, bool) {
	if !e2ee {
		if body.mls != nil {
			writeE2EENotEnabled(w, r, "this channel is not end-to-end encrypted")
			return nil, false
		}
		return nil, true
	}

	if body.mls == nil {
		writeError(w, r, http.StatusBadRequest, codeE2EERequired,
			"this channel is end-to-end encrypted; mls is required")
		return nil, false
	}
	if body.content != "" {
		writeError(w, r, http.StatusBadRequest, codeE2EERequired,
			"this channel is end-to-end encrypted; content must be empty")
		return nil, false
	}
	if body.mls.Epoch < 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "mls.epoch must not be negative")
		return nil, false
	}
	ciphertext, ok := decodeBlob(w, r, body.mls.Ciphertext, "mls.ciphertext", maxCiphertextB64)
	if !ok {
		return nil, false
	}
	mentions, ok := declaredMentions(w, r, body.mls.Mentions)
	if !ok {
		return nil, false
	}
	return &storage.MessageMls{Epoch: body.mls.Epoch, Ciphertext: ciphertext, Mentions: mentions}, true
}

// declaredMentions takes ADR 014's sender-declared notification routing off
// the envelope, bounded by the contract's maxItems.
//
// The length is the only thing this can refuse, and refusing it is the only
// reason the function exists. Everything else a declaration can get wrong —
// naming somebody who left the channel, somebody who was never in it, no user
// at all, or the same person twice — is dropped by the channel_members join
// inside the write's own statement, which is where it has to be decided: a
// handler that checked membership here would be checking it against a roster
// that can change before the insert lands. An unbounded list is different in
// kind: nothing downstream bounds it, and a hundred thousand ids would be a
// hundred thousand parameters on the hottest write path.
//
// What it deliberately does not do is validate the declaration against the
// message. The server holds no plaintext to check it against (ADR 006), so
// the list is advisory routing, not a claim about content — the trust framing
// ADR 014 fixes so nobody re-derives a stricter one here and finds they cannot
// implement it.
func declaredMentions(w http.ResponseWriter, r *http.Request, declared *[]uuid.UUID) ([]uuid.UUID, bool) {
	if declared == nil {
		return nil, true
	}
	if len(*declared) > maxDeclaredMentions {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("mls.mentions must name at most %d members", maxDeclaredMentions))
		return nil, false
	}
	return *declared, true
}

// e2eeAtBirth decides the encryption a conversation created now must carry,
// from the organisation's mode and the request's assertion (ADR 011).
//
// Omitted means "whatever the mode says". A value that agrees is fine. A
// value that disagrees is refused with the mode's own code — never coerced,
// because the flag fixes a property no later request can change, and a
// client that asked for the opposite of what it gets has been told nothing.
//
// It is one function called from both creation choke points for the same
// reason e2eeBody is one function: two copies of a birth rule are two
// chances for the channel path and the direct-message path to disagree, and
// the disagreement would show up as exactly one way to create a conversation
// the mode forbids.
//
// The mode is read here rather than passed in, so no caller can supply one.
func (s *apiServer) e2eeAtBirth(w http.ResponseWriter, r *http.Request, store Store, asserted *bool) (bool, bool) {
	mode, err := store.EncryptionMode(r.Context())
	if err != nil {
		internalError(w, r, err)
		return false, false
	}

	// Strict is the only mode that encrypts, and anything that is not
	// literally compliance is treated as strict: an unreadable mode must fail
	// towards encryption, never away from it.
	encrypted := mode != storage.EncryptionModeCompliance
	if asserted == nil || *asserted == encrypted {
		return encrypted, true
	}
	if encrypted {
		writeError(w, r, http.StatusBadRequest, codeE2EERequiredByOrg,
			"this organisation creates only end-to-end encrypted conversations")
	} else {
		writeError(w, r, http.StatusBadRequest, codeE2EEForbiddenByOrg,
			"this organisation creates only conversations it can retain and export")
	}
	return false, false
}
