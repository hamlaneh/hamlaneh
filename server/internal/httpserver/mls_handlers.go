package httpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	openapitypes "github.com/oapi-codegen/runtime/types"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The E2EE transport (ADR 006, openapi.yaml "E2EE transport"). Eight
// endpoints, and the server's entire MLS vocabulary.
//
// It is deliberately illiterate. Every payload below is base64 on the wire
// and bytea in storage, decoded here only to check that it IS base64 and to
// bound its size — never to look inside. The one structured claim a client
// makes, the epoch on a commit, is used for first-wins conflict rejection and
// for nothing else, and it is safe to leave unverified: a client that lies
// about its epoch gets its commit refused by its own group members, who
// verify cryptographically. Garbage in any blob harms only the client that
// uploaded it.
//
// Authorization splits in two, on purpose (ADR 006 decision 2). The
// channel-scoped endpoints go through resolveChannel and authz.Can exactly
// like every other channel path — same membership rule, same 404 for a
// stranger, same matrix rows — because transport is what this server is the
// authority on. Who can actually READ a group is the group's own business,
// and the server does not try to enforce that the two agree, because
// verifying it would mean reading group state, which is the capability this
// design denies it.

// Contract bounds for the E2EE transport (openapi.yaml). They are transport
// hygiene rather than validation: they bound storage and frame sizes, and say
// nothing about whether a blob is a well-formed MLS artifact.
const (
	maxSignatureKeyB64   = 200
	maxKeyPackageB64     = 8192
	maxKeyPackages       = 50
	maxGroupIDB64        = 96
	maxCommitB64         = 350000
	maxWelcomeB64        = 350000
	maxWelcomesPerCommit = 200
	maxCiphertextB64     = 45000
	maxCommitPageLimit   = 50
)

// maxKeyPackagesBody bounds the replace-all request. The contract's own
// arithmetic is 50 packages of 8192 characters, so 512 KiB clears it with
// room for the JSON around it — and an authenticated caller can already spend
// far more than this on an upload.
const maxKeyPackagesBody int64 = 512 << 10

// maxCommitBody bounds a commit submission, and it is a deliberate refusal to
// honour the contract's arithmetic maximum.
//
// That maximum is 350000 characters of commit plus 200 Welcomes of 350000
// each: roughly 70 MB in one JSON body, which has to be held in memory to be
// decoded. No MLS deployment produces it — a Welcome is kilobytes, and the
// 350000 cap on one exists to be far past the real number rather than to be
// reachable — but a caller who simply sends it would cost this server 70 MB
// per request.
//
// 8 MiB is what fits the real worst case with a wide margin: a 50-member
// bootstrap is 50 Welcomes, and even at 40 KiB each that is 2 MB. A request
// above this answers 400 rather than being served, which IS a narrowing of
// the contract and is reported as one — the fix belongs in openapi.yaml,
// either as a smaller per-Welcome cap or as a stated total.
const maxCommitBody int64 = 8 << 20

// RegisterMlsDevice records this client instance as an MLS device.
//
// Idempotent on (user, signature_public_key): re-registering the same key
// answers 200 with the device that already exists, so a client can call this
// on every startup without bookkeeping. The device is always the caller's —
// nothing in the request names a user — so no caller can register a leaf
// under somebody else's account.
func (s *apiServer) RegisterMlsDevice(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.selfScope(w, r, "register mls device")
	if !ok {
		return
	}

	var req api.RegisterMlsDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	key, ok := decodeBlob(w, r, req.SignaturePublicKey, "signature_public_key", maxSignatureKeyB64)
	if !ok {
		return
	}

	device, created, err := store.RegisterMlsDevice(r.Context(), prin.user.ID, key)
	if err != nil {
		internalError(w, r, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSONValue(w, r, status, api.MlsDevice{
		Id:                 device.ID,
		SignaturePublicKey: base64.StdEncoding.EncodeToString(device.SignaturePublicKey),
		CreatedAt:          device.CreatedAt,
	})
}

// ReplaceMlsKeyPackages replaces one device's pool of unclaimed key packages.
//
// Own-device only. Another user's device id — and the caller's own other
// device — answers the same 404, so the endpoint cannot be used to learn
// which device ids exist. The server never checks that a package matches the
// device's signature key: a client uploading garbage only makes itself
// unaddable, and checking would mean parsing MLS.
func (s *apiServer) ReplaceMlsKeyPackages(w http.ResponseWriter, r *http.Request, deviceID api.MlsDeviceId) {
	prin, store, ok := s.selfScope(w, r, "replace mls key packages")
	if !ok {
		return
	}

	var req api.ReplaceMlsKeyPackagesRequest
	if !decodeJSONLimit(w, r, &req, maxKeyPackagesBody) {
		return
	}
	if len(req.KeyPackages) < 1 || len(req.KeyPackages) > maxKeyPackages {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("key_packages must hold 1 to %d packages", maxKeyPackages))
		return
	}
	packages := make([][]byte, 0, len(req.KeyPackages))
	for _, encoded := range req.KeyPackages {
		blob, blobOK := decodeBlob(w, r, encoded, "key_packages", maxKeyPackageB64)
		if !blobOK {
			return
		}
		packages = append(packages, blob)
	}

	count, err := store.ReplaceMlsKeyPackages(r.Context(), prin.user.ID, deviceID, packages)
	if errors.Is(err, storage.ErrMlsDeviceNotFound) {
		writeError(w, r, http.StatusNotFound, codeMlsDeviceNotFound, "no such device")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, api.MlsKeyPackagePool{UnclaimedCount: count})
}

// GetMlsGroup returns the channel's MLS group.
//
// A member of an e2ee channel with no group yet gets 404 mls_group_not_found,
// which is the signal to create one; a non-member gets the channel's own 404,
// as on every channel-scoped path. The two codes differ, and both are 404, so
// a stranger learns nothing about whether the channel is encrypted.
func (s *apiServer) GetMlsGroup(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.channelMlsScope(w, r, channelID, authz.MlsRead)
	if !ok {
		return
	}

	group, err := sc.store.MlsGroupByChannel(r.Context(), channelID)
	if errors.Is(err, storage.ErrMlsGroupNotFound) {
		writeMlsGroupNotFound(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiMlsGroup(group))
}

// CreateMlsGroup registers the channel's group at epoch 0.
//
// Exactly one per channel: a concurrent second create answers 409
// mls_group_exists and that client waits to be added instead. Refused on a
// channel without e2ee — a group on a plaintext channel would be state
// nothing may ever use, and answering 400 rather than creating it is what
// keeps the channel's mode the single source of whether encryption is in
// play.
func (s *apiServer) CreateMlsGroup(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.channelMlsScope(w, r, channelID, authz.MlsWrite)
	if !ok {
		return
	}
	if !sc.channel.E2EE {
		writeE2EENotEnabled(w, r, "this channel is not end-to-end encrypted")
		return
	}

	var req api.CreateMlsGroupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	groupID, ok := decodeBlob(w, r, req.GroupId, "group_id", maxGroupIDB64)
	if !ok {
		return
	}

	group, err := sc.store.CreateMlsGroup(r.Context(), channelID, groupID)
	switch {
	case errors.Is(err, storage.ErrMlsGroupExists):
		writeError(w, r, http.StatusConflict, codeMlsGroupExists,
			"this channel already has an MLS group")
		return
	case errors.Is(err, storage.ErrChannelNotFound):
		// The channel went away between the authorization check and the
		// insert; the caller learns exactly what a stranger would.
		writeChannelNotFound(w, r)
		return
	case err != nil:
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusCreated, apiMlsGroup(group))
}

// ClaimMlsKeyPackages consumes one key package per device of a member.
//
// Channel-scoped rather than a public directory, deliberately: the caller
// must be a member and the target must be a member of this channel, which
// kills the enumerate-and-exhaust surface an open fetch would carry. A
// non-member target answers 404 member_not_found — distinct from the
// channel's own 404, because the caller can already see the channel and what
// they got wrong is who they named.
func (s *apiServer) ClaimMlsKeyPackages(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.channelMlsScope(w, r, channelID, authz.MlsWrite)
	if !ok {
		return
	}

	var req api.ClaimMlsKeyPackagesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UserId == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "user_id is required")
		return
	}

	claims, missing, err := sc.store.ClaimMlsKeyPackages(r.Context(), channelID, req.UserId)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeMemberNotFound,
			"no such member of this channel")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	out := api.MlsKeyPackageClaims{
		Claims:           make([]api.MlsKeyPackageClaim, 0, len(claims)),
		MissingDeviceIds: make([]openapitypes.UUID, 0, len(missing)),
	}
	for _, c := range claims {
		out.Claims = append(out.Claims, api.MlsKeyPackageClaim{
			DeviceId:   c.DeviceID,
			KeyPackage: base64.StdEncoding.EncodeToString(c.KeyPackage),
		})
	}
	out.MissingDeviceIds = append(out.MissingDeviceIds, missing...)
	writeJSONValue(w, r, http.StatusOK, out)
}

// ListMlsCommits returns the commit log after an epoch, ascending.
//
// A channel with no group has an empty log rather than a 404: the log is a
// read, and whether a group exists is what GET .../mls/group answers. A
// client that is caught up and one whose channel has no group both see an
// empty page, and both are correct — there is nothing to apply either way.
func (s *apiServer) ListMlsCommits(w http.ResponseWriter, r *http.Request, channelID api.ChannelId, params api.ListMlsCommitsParams) {
	sc, ok := s.channelMlsScope(w, r, channelID, authz.MlsRead)
	if !ok {
		return
	}
	if params.AfterEpoch < 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "after_epoch must not be negative")
		return
	}
	limit, ok := pageLimit(w, r, params.Limit, maxCommitPageLimit, maxCommitPageLimit)
	if !ok {
		return
	}

	commits, err := sc.store.ListMlsCommits(r.Context(), channelID, params.AfterEpoch, limit)
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := api.MlsCommitPage{Commits: make([]api.MlsCommit, 0, len(commits))}
	for _, c := range commits {
		page.Commits = append(page.Commits, api.MlsCommit{
			Epoch:     c.Epoch,
			Message:   base64.StdEncoding.EncodeToString(c.Message),
			CreatedAt: c.CreatedAt,
		})
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// SubmitMlsCommit is the sequencing point of the whole design (ADR 006).
//
// The client names the epoch its commit was built at; storage accepts it only
// while that is still the group's current epoch, advancing by exactly one. Of
// two concurrent committers one wins and the other gets 409
// mls_epoch_conflict, refetches the log, merges and retries. The Welcomes
// this commit carries are stored in the same transaction, because a committed
// add whose Welcome was lost is a forked group.
//
// The two events go out after the transaction committed, never before: an
// mls_commit announcing an epoch a rollback erased would send every member to
// fetch a log that does not have it.
func (s *apiServer) SubmitMlsCommit(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.channelMlsScope(w, r, channelID, authz.MlsWrite)
	if !ok {
		return
	}

	var req api.SubmitMlsCommitRequest
	if !decodeJSONLimit(w, r, &req, maxCommitBody) {
		return
	}
	if req.Epoch < 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "epoch must not be negative")
		return
	}
	message, ok := decodeBlob(w, r, req.Message, "message", maxCommitB64)
	if !ok {
		return
	}
	welcomes, ok := commitWelcomes(w, r, req)
	if !ok {
		return
	}

	outcome, err := sc.store.SubmitMlsCommit(r.Context(), storage.NewMlsCommit{
		ChannelID: channelID,
		Epoch:     req.Epoch,
		Message:   message,
		Welcomes:  welcomes,
	})
	switch {
	case errors.Is(err, storage.ErrMlsEpochConflict):
		writeError(w, r, http.StatusConflict, codeMlsEpochConflict,
			"the named epoch is no longer current")
		return
	case errors.Is(err, storage.ErrMlsGroupNotFound):
		writeMlsGroupNotFound(w, r)
		return
	case errors.Is(err, storage.ErrMlsDeviceNotFound):
		// A Welcome addressed to a device that does not exist. Nothing was
		// stored: the insert shares the commit's transaction, so the epoch
		// did not move either.
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"every welcome must name a registered device")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	s.realtime.MlsCommit(channelID, outcome.Epoch)
	for _, userID := range outcome.WelcomeUserIDs {
		s.realtime.MlsWelcome(userID)
	}
	w.WriteHeader(http.StatusCreated)
}

// commitWelcomes validates the Welcomes riding along with a commit.
//
// Only the shape rules live here: how many, distinct devices, and decodable
// blobs within their cap. Whether a device id names anything is not a
// question a handler can answer without racing the answer, so it is left to
// the foreign key inside the commit's own transaction.
//
// Duplicate device ids are a 400 rather than a silent de-duplication, the
// same call the send path makes about attachment ids: two Welcomes for one
// device is a client bug, and storing both would leave that device a queue of
// Welcomes to one group where the protocol expects one.
func commitWelcomes(w http.ResponseWriter, r *http.Request, req api.SubmitMlsCommitRequest) ([]storage.MlsWelcomeDelivery, bool) {
	if req.Welcomes == nil {
		return nil, true
	}
	deliveries := *req.Welcomes
	if len(deliveries) > maxWelcomesPerCommit {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("a commit may carry at most %d welcomes", maxWelcomesPerCommit))
		return nil, false
	}

	seen := make(map[uuid.UUID]bool, len(deliveries))
	out := make([]storage.MlsWelcomeDelivery, 0, len(deliveries))
	for _, d := range deliveries {
		if d.DeviceId == uuid.Nil || seen[d.DeviceId] {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"welcomes must name distinct device ids")
			return nil, false
		}
		seen[d.DeviceId] = true

		blob, ok := decodeBlob(w, r, d.Welcome, "welcomes[].welcome", maxWelcomeB64)
		if !ok {
			return nil, false
		}
		out = append(out, storage.MlsWelcomeDelivery{DeviceID: d.DeviceId, Welcome: blob})
	}
	return out, true
}

// ListMlsWelcomes returns every Welcome waiting for any of the caller's
// devices.
//
// All of their devices on any of their sockets: a Welcome is encrypted to one
// device's key package, so a sibling device holds bytes it cannot open and
// the fan-out reveals nothing. Explicit acknowledgement rather than
// delete-on-read, so a client that fetches and crashes before joining finds
// its Welcome still there.
func (s *apiServer) ListMlsWelcomes(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.selfScope(w, r, "list mls welcomes")
	if !ok {
		return
	}

	welcomes, err := store.ListMlsWelcomes(r.Context(), prin.user.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	out := api.MlsWelcomeList{Welcomes: make([]api.MlsWelcome, 0, len(welcomes))}
	for _, wl := range welcomes {
		out.Welcomes = append(out.Welcomes, api.MlsWelcome{
			Id:        wl.ID,
			ChannelId: wl.ChannelID,
			GroupId:   base64.StdEncoding.EncodeToString(wl.GroupID),
			DeviceId:  wl.DeviceID,
			Welcome:   base64.StdEncoding.EncodeToString(wl.Welcome),
			CreatedAt: wl.CreatedAt,
		})
	}
	writeJSONValue(w, r, http.StatusOK, out)
}

// AcknowledgeMlsWelcome drops a Welcome the caller's device has joined with.
//
// Idempotent: acknowledging one that is already gone is 204 again, which is
// the state the caller asked for. Another user's Welcome answers 404 — the
// contract distinguishes the two.
func (s *apiServer) AcknowledgeMlsWelcome(w http.ResponseWriter, r *http.Request, welcomeID api.MlsWelcomeId) {
	prin, store, ok := s.selfScope(w, r, "acknowledge mls welcome")
	if !ok {
		return
	}

	err := store.DeleteMlsWelcome(r.Context(), prin.user.ID, welcomeID)
	if errors.Is(err, storage.ErrMlsWelcomeNotFound) {
		writeError(w, r, http.StatusNotFound, codeMlsWelcomeNotFound, "no such welcome")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// selfScope resolves the caller and the store for an endpoint under
// /api/v1/users/me: the whole authorization story of those four, because the
// subject of every one of them is the session's own user and nothing in any
// request selects a different one.
func (s *apiServer) selfScope(w http.ResponseWriter, r *http.Request, what string) (principal, Store, bool) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, fmt.Errorf("%s reached without principal", what))
		return principal{}, nil, false
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return principal{}, nil, false
	}
	return prin, store, true
}

// channelMlsScope resolves a channel-scoped MLS request and makes its
// authorization decision in one place.
//
// action is MlsRead for the two reads and MlsWrite for the three writes. Both
// resolve to "any member" today — that is ADR 006's transport rule, and the
// confidentiality half is the group's own, which this server structurally
// cannot enforce — but they are asked as two questions so a later channel
// role model has somewhere to say "may read this group" without also saying
// "may move its epoch".
//
// A non-member gets the channel's own 404 here exactly as everywhere else, so
// the refusal never reveals that the channel exists, let alone that it is
// encrypted. Doing this once rather than in each handler is what stops one of
// the five growing a different answer.
func (s *apiServer) channelMlsScope(w http.ResponseWriter, r *http.Request, channelID uuid.UUID, action authz.Action) (channelScope, bool) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return channelScope{}, false
	}
	if !authz.Can(r.Context(), &sc.prin.user, action, sc.resource()) {
		sc.deny(w, r)
		return channelScope{}, false
	}
	return sc, true
}

// apiMlsGroup maps a stored group onto the contract's MlsGroup.
func apiMlsGroup(group storage.MlsGroup) api.MlsGroup {
	return api.MlsGroup{
		GroupId:   base64.StdEncoding.EncodeToString(group.GroupID),
		Epoch:     group.Epoch,
		CreatedAt: group.CreatedAt,
	}
}

// decodeBlob turns one contract base64 field into the bytes storage keeps.
//
// The bounds are on the ENCODED string, because that is what the contract
// states and what bounds the request; the decode that follows checks that the
// field is base64 at all and nothing else. Standard encoding with padding,
// which is what a browser's btoa produces and what every serialization on the
// way out uses — one alphabet, so a blob round-trips byte for byte.
//
// It is the only place an MLS payload is looked at, and all it learns is the
// length.
func decodeBlob(w http.ResponseWriter, r *http.Request, encoded, field string, maxLen int) ([]byte, bool) {
	// The minimum is 1 on every field the contract has here, and it is
	// written in rather than passed: an empty blob is never a valid MLS
	// artifact, so a call site that wanted to allow one would be a contract
	// change rather than a different argument.
	if encoded == "" || len(encoded) > maxLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("%s must be 1 to %d base64 characters", field, maxLen))
		return nil, false
	}
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("%s must be base64", field))
		return nil, false
	}
	return blob, true
}

// writeMlsGroupNotFound is the single answer to "this channel has no MLS
// group": one call site, so the create signal cannot drift into two shapes.
func writeMlsGroupNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeMlsGroupNotFound, "this channel has no MLS group")
}

// writeE2EENotEnabled answers an encrypted operation on a plaintext channel.
func writeE2EENotEnabled(w http.ResponseWriter, r *http.Request, message string) {
	writeError(w, r, http.StatusBadRequest, codeE2EENotEnabled, message)
}
