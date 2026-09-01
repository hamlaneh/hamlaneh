package httpserver

import (
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The recovery surface (ADR 010, openapi.yaml): three endpoints for the sealed
// backup envelope, and one for dropping a lost device out of the directory.
//
// All four hang under /api/v1/users/me, so all four go through selfScope and
// nothing more: the subject is the session's own user, and no request on any
// of them carries an id that could name a different account. The one path
// parameter here — the device id on the deregistration — is scoped inside the
// SQL rather than checked beside it, so another account's device answers
// exactly what an id naming nothing answers.
//
// The envelope is opaque in the way the rest of this surface's blobs are, and
// then some: it is decoded to confirm it is base64 and to bound it, and there
// is no key anywhere in this process that could open it. `counter` is read,
// and read for exactly one purpose — refusing a write that does not move
// forward. That refusal is a convenience rail and the contract says so; the
// anti-rollback control is the client comparing the counter SEALED in the
// envelope's authenticated header against a floor it keeps itself, which is
// the comparison this server cannot lie its way past.

// maxBackupEnvelopeB64 is the contract's cap on the envelope field: ~512 KiB
// raw. Verification records are kilobytes, and the endpoint is not a blob
// store (openapi.yaml, PutMlsBackupRequest.envelope).
const maxBackupEnvelopeB64 = 700000

// maxBackupBody bounds the JSON body around that field. One megabyte clears
// 700000 characters of base64 plus its envelope of JSON several times over,
// and a body has to be held in memory to be decoded.
const maxBackupBody int64 = 1 << 20

// PutMlsBackup stores this account's sealed verification backup.
//
// One row per account, replaced in place. A counter that does not exceed the
// stored one is refused with 409 mls_backup_stale — the ordinary lost update
// between two of the owner's own devices, which is all this check claims to
// stop.
func (s *apiServer) PutMlsBackup(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.selfScope(w, r, "put mls backup")
	if !ok {
		return
	}

	var req api.PutMlsBackupRequest
	if !decodeJSONLimit(w, r, &req, maxBackupBody) {
		return
	}
	// The contract's minimum is 1 (openapi.yaml). A counter of zero or below
	// is not a stale write to refuse with 409 — it is a malformed request, and
	// answering it as a conflict would tell a client to retry with a higher
	// number when what it needs is to fix its framing.
	if req.Counter < 1 {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "counter must be at least 1")
		return
	}
	envelope, ok := decodeBlob(w, r, req.Envelope, "envelope", maxBackupEnvelopeB64)
	if !ok {
		return
	}

	err := store.PutMlsBackup(r.Context(), prin.user.ID, envelope, req.Counter)
	if errors.Is(err, storage.ErrMlsBackupStale) {
		writeError(w, r, http.StatusConflict, codeMlsBackupStale,
			"a newer backup is already stored for this account")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMlsBackup returns this account's sealed backup, if one exists.
//
// 404 mls_backup_not_found when there is none, which is a state a person can
// genuinely be in: they never enrolled, or they turned backup off. The restore
// screen states that answer plainly rather than implying a lost key — and
// treats it as worth suspicion when the person knows they made one, because
// deletion and never-backed-up are indistinguishable on the wire (ADR 010,
// decision 3).
func (s *apiServer) GetMlsBackup(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.selfScope(w, r, "get mls backup")
	if !ok {
		return
	}

	backup, err := store.MlsBackupByUser(r.Context(), prin.user.ID)
	if errors.Is(err, storage.ErrMlsBackupNotFound) {
		writeError(w, r, http.StatusNotFound, codeMlsBackupNotFound,
			"this account has no stored backup")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, api.MlsBackup{
		Envelope:  base64.StdEncoding.EncodeToString(backup.Envelope),
		Counter:   backup.Counter,
		UpdatedAt: backup.UpdatedAt,
	})
}

// DeleteMlsBackup forgets this account's backup. Always 204, including for an
// account that has none: the caller asked for a state, and it is already true.
//
// Offering it changes nothing about what the server can do — deleting is the
// one direction a hostile server can take unilaterally anyway — and quite a
// lot about what a person can.
func (s *apiServer) DeleteMlsBackup(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.selfScope(w, r, "delete mls backup")
	if !ok {
		return
	}

	if err := store.DeleteMlsBackup(r.Context(), prin.user.ID); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeregisterMlsDevice drops one of this account's devices from the directory —
// the lost-device write.
//
// Without it a stolen or discarded device's signature key stays in the
// directory, and therefore inside every group's allow-list, for as long as the
// account exists. ADR 007's sweep evicts exactly what the directory stops
// listing, so this write is what makes the sweep able to act; the eviction
// itself happens on every other member's next reconcile, in their client, and
// nothing here can or should force it.
//
// It does NOT revoke sessions and does not touch messages. Signing a device
// out and un-listing its key answer different questions, and somebody who has
// lost a laptop needs both — folding them together here would make one act
// silently do the other, which is how a person ends up believing they did the
// half they did not do.
func (s *apiServer) DeregisterMlsDevice(w http.ResponseWriter, r *http.Request, deviceID api.MlsDeviceId) {
	prin, store, ok := s.selfScope(w, r, "deregister mls device")
	if !ok {
		return
	}

	err := store.DeregisterMlsDevice(r.Context(), prin.user.ID, deviceID)
	if errors.Is(err, storage.ErrMlsDeviceNotFound) {
		writeError(w, r, http.StatusNotFound, codeMlsDeviceNotFound, "no such device")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
