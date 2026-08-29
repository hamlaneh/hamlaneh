package httpserver_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The eight E2EE transport handlers (ADR 006). What these pin is the contract
// semantics the storage tests cannot see: which status each outcome carries,
// which code, and — on the channel-scoped five — that the refusals arrive in
// the order that keeps a stranger on the channel's own 404.

// mlsBlobB64 is a valid base64 payload. The server never parses one.
var mlsBlobB64 = base64.StdEncoding.EncodeToString([]byte("blob"))

const (
	mlsDeviceID  = "aaaaaaaa-0000-4000-8000-000000000001"
	mlsWelcomeID = "bbbbbbbb-0000-4000-8000-000000000001"
)

func mlsDeviceUUID() uuid.UUID  { return uuid.MustParse(mlsDeviceID) }
func mlsWelcomeUUID() uuid.UUID { return uuid.MustParse(mlsWelcomeID) }

func TestRegisterMlsDevice(t *testing.T) {
	t.Parallel()

	t.Run("a fresh key is 201 and a repeat is 200", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotUser uuid.UUID
		var gotKey []byte
		created := true
		store.registerMlsDevice = func(_ context.Context, userID uuid.UUID, key []byte) (storage.MlsDevice, bool, error) {
			gotUser, gotKey = userID, key
			return storage.MlsDevice{
				ID: mlsDeviceUUID(), UserID: userID, SignaturePublicKey: key,
				CreatedAt: time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
			}, created, nil
		}

		rec := do(t, store, request(http.MethodPost, "/api/v1/users/me/mls/device",
			fmt.Sprintf(`{"signature_public_key":%q}`, mlsBlobB64),
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		// The device is the SESSION's user, never one the request named:
		// there is nothing in the body that could select another account.
		if gotUser != fixtureUser().ID {
			t.Errorf("registered under %s, want the session's user %s", gotUser, fixtureUser().ID)
		}
		if string(gotKey) != "blob" {
			t.Errorf("storage got key %q, want the decoded bytes", gotKey)
		}

		var device api.MlsDevice
		if err := json.Unmarshal(rec.Body.Bytes(), &device); err != nil {
			t.Fatalf("body is not an MlsDevice: %v", err)
		}
		if device.SignaturePublicKey != mlsBlobB64 {
			t.Errorf("echoed key %q, want it as registered", device.SignaturePublicKey)
		}

		created = false
		rec = do(t, store, request(http.MethodPost, "/api/v1/users/me/mls/device",
			fmt.Sprintf(`{"signature_public_key":%q}`, mlsBlobB64),
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Errorf("re-registering got status %d, want 200", rec.Code)
		}
	})

	t.Run("refuses a malformed key", func(t *testing.T) {
		t.Parallel()
		for name, body := range map[string]string{
			"not base64": `{"signature_public_key":"not base64!"}`,
			"empty":      `{"signature_public_key":""}`,
			"over cap":   fmt.Sprintf(`{"signature_public_key":%q}`, strings.Repeat("A", 204)),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				// registerMlsDevice stays unwired: reaching it is a 500.
				rec := do(t, authedStore(fixtureUser()),
					request(http.MethodPost, "/api/v1/users/me/mls/device", body,
						withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestReplaceMlsKeyPackages(t *testing.T) {
	t.Parallel()
	path := "/api/v1/users/me/mls/devices/" + mlsDeviceID + "/key-packages"

	t.Run("replaces the pool and reports its size", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotUser, gotDevice uuid.UUID
		var gotPackages [][]byte
		store.replaceMlsKeyPackages = func(_ context.Context, userID, deviceID uuid.UUID, packages [][]byte) (int, error) {
			gotUser, gotDevice, gotPackages = userID, deviceID, packages
			return len(packages), nil
		}

		rec := do(t, store, request(http.MethodPut, path,
			fmt.Sprintf(`{"key_packages":[%q,%q]}`, mlsBlobB64, mlsBlobB64),
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotUser != fixtureUser().ID || gotDevice != mlsDeviceUUID() {
			t.Errorf("storage got (%s, %s), want the session's user and the path's device", gotUser, gotDevice)
		}
		if len(gotPackages) != 2 || string(gotPackages[0]) != "blob" {
			t.Errorf("storage got %q, want two decoded packages", gotPackages)
		}
		var pool api.MlsKeyPackagePool
		if err := json.Unmarshal(rec.Body.Bytes(), &pool); err != nil {
			t.Fatalf("body is not an MlsKeyPackagePool: %v", err)
		}
		if pool.UnclaimedCount != 2 {
			t.Errorf("unclaimed_count = %d, want 2", pool.UnclaimedCount)
		}
	})

	t.Run("another user's device is 404", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.replaceMlsKeyPackages = func(context.Context, uuid.UUID, uuid.UUID, [][]byte) (int, error) {
			return 0, storage.ErrMlsDeviceNotFound
		}

		rec := do(t, store, request(http.MethodPut, path,
			fmt.Sprintf(`{"key_packages":[%q]}`, mlsBlobB64),
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "mls_device_not_found")
	})

	t.Run("refuses a malformed batch", func(t *testing.T) {
		t.Parallel()
		oversized := make([]string, 51)
		for i := range oversized {
			oversized[i] = `"` + mlsBlobB64 + `"`
		}
		for name, body := range map[string]string{
			"empty list":                   `{"key_packages":[]}`,
			"too many":                     `{"key_packages":[` + strings.Join(oversized, ",") + `]}`,
			"a package that is not base64": `{"key_packages":["not base64!"]}`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				rec := do(t, authedStore(fixtureUser()),
					request(http.MethodPut, path, body, withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestGetMlsGroup(t *testing.T) {
	t.Parallel()

	t.Run("a member of a channel with a group gets it", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.mlsGroupByChannel = func(context.Context, uuid.UUID) (storage.MlsGroup, error) {
			return storage.MlsGroup{
				ChannelID: channelUUID(), GroupID: []byte("blob"), Epoch: 3,
				CreatedAt: time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
			}, nil
		}

		rec := do(t, store, request(http.MethodGet, channelPath("/mls/group"), "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var group api.MlsGroup
		if err := json.Unmarshal(rec.Body.Bytes(), &group); err != nil {
			t.Fatalf("body is not an MlsGroup: %v", err)
		}
		if group.GroupId != mlsBlobB64 || group.Epoch != 3 {
			t.Errorf("group = %+v, want the stored one as base64", group)
		}
	})

	t.Run("no group yet is the create signal", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.mlsGroupByChannel = func(context.Context, uuid.UUID) (storage.MlsGroup, error) {
			return storage.MlsGroup{}, storage.ErrMlsGroupNotFound
		}

		rec := do(t, store, request(http.MethodGet, channelPath("/mls/group"), "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "mls_group_not_found")
	})

	t.Run("a stranger gets the channel's own 404", func(t *testing.T) {
		t.Parallel()
		// mlsGroupByChannel stays unwired: a non-member must be refused
		// before anything is read on their behalf, so reaching it is a 500.
		store := channelStore(fixtureUser(), encryptedChannel(), false)
		rec := do(t, store, request(http.MethodGet, channelPath("/mls/group"), "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})
}

func TestCreateMlsGroup(t *testing.T) {
	t.Parallel()
	body := fmt.Sprintf(`{"group_id":%q}`, mlsBlobB64)

	t.Run("registers the group at epoch 0", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		var gotGroupID []byte
		store.createMlsGroup = func(_ context.Context, _ uuid.UUID, groupID []byte) (storage.MlsGroup, error) {
			gotGroupID = groupID
			return storage.MlsGroup{ChannelID: channelUUID(), GroupID: groupID}, nil
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/mls/group"), body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if string(gotGroupID) != "blob" {
			t.Errorf("storage got group id %q, want the decoded bytes", gotGroupID)
		}
	})

	t.Run("the create race's loser is 409", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.createMlsGroup = func(context.Context, uuid.UUID, []byte) (storage.MlsGroup, error) {
			return storage.MlsGroup{}, storage.ErrMlsGroupExists
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/mls/group"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusConflict, "mls_group_exists")
	})

	t.Run("a plaintext channel is refused", func(t *testing.T) {
		t.Parallel()
		// createMlsGroup stays unwired: a group on a plaintext channel must
		// never be written, so reaching storage is a 500.
		store := memberStore()
		rec := do(t, store, request(http.MethodPost, channelPath("/mls/group"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "e2ee_not_enabled")
	})

	t.Run("a stranger to an encrypted channel learns nothing about its mode", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), encryptedChannel(), false)
		rec := do(t, store, request(http.MethodPost, channelPath("/mls/group"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})
}

func TestClaimMlsKeyPackages(t *testing.T) {
	t.Parallel()
	body := `{"user_id":"` + testPeerID + `"}`

	t.Run("returns one package per addressable device", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		var gotChannel, gotTarget uuid.UUID
		store.claimMlsKeyPackages = func(_ context.Context, channelID, target uuid.UUID) ([]storage.MlsKeyPackageClaim, []uuid.UUID, error) {
			gotChannel, gotTarget = channelID, target
			return []storage.MlsKeyPackageClaim{{DeviceID: mlsDeviceUUID(), KeyPackage: []byte("blob")}},
				[]uuid.UUID{mlsWelcomeUUID()}, nil
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/mls/key-package-claims"), body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotChannel != channelUUID() || gotTarget != peerUUID() {
			t.Errorf("storage got (%s, %s), want the path's channel and the body's target", gotChannel, gotTarget)
		}

		var claims api.MlsKeyPackageClaims
		if err := json.Unmarshal(rec.Body.Bytes(), &claims); err != nil {
			t.Fatalf("body is not MlsKeyPackageClaims: %v", err)
		}
		if len(claims.Claims) != 1 || claims.Claims[0].KeyPackage != mlsBlobB64 {
			t.Errorf("claims = %+v, want the one package as base64", claims.Claims)
		}
		if len(claims.MissingDeviceIds) != 1 {
			t.Errorf("missing_device_ids = %v, want the empty-pool device", claims.MissingDeviceIds)
		}
	})

	t.Run("both lists are arrays even when empty", func(t *testing.T) {
		t.Parallel()
		// The contract makes both required, and a JSON null where a client
		// expects an array is a crash rather than an empty list.
		store := encryptedMemberStore()
		store.claimMlsKeyPackages = func(context.Context, uuid.UUID, uuid.UUID) ([]storage.MlsKeyPackageClaim, []uuid.UUID, error) {
			return nil, nil, nil
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/mls/key-package-claims"), body,
			withSessionCookie("tok"), withCSRF()))
		if got := rec.Body.String(); !strings.Contains(got, `"claims":[]`) ||
			!strings.Contains(got, `"missing_device_ids":[]`) {
			t.Errorf("body %s, want both fields as empty arrays", got)
		}
	})

	t.Run("a target who is not a member is 404 member_not_found", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.claimMlsKeyPackages = func(context.Context, uuid.UUID, uuid.UUID) ([]storage.MlsKeyPackageClaim, []uuid.UUID, error) {
			return nil, nil, storage.ErrNotFound
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/mls/key-package-claims"), body,
			withSessionCookie("tok"), withCSRF()))
		// Distinct from the channel's own 404: the caller can see the
		// channel, and what they got wrong is who they named.
		wantError(t, rec, http.StatusNotFound, "member_not_found")
	})

	t.Run("a stranger to the channel is refused first", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), encryptedChannel(), false)
		rec := do(t, store, request(http.MethodPost, channelPath("/mls/key-package-claims"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})
}

func TestListMlsCommits(t *testing.T) {
	t.Parallel()

	t.Run("pages by epoch", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		var gotAfter int64
		var gotLimit int
		store.listMlsCommits = func(_ context.Context, _ uuid.UUID, afterEpoch int64, limit int) ([]storage.MlsCommit, error) {
			gotAfter, gotLimit = afterEpoch, limit
			return []storage.MlsCommit{{Epoch: 4, Message: []byte("blob")}}, nil
		}

		rec := do(t, store, request(http.MethodGet,
			channelPath("/mls/commits")+"?after_epoch=3&limit=10", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotAfter != 3 || gotLimit != 10 {
			t.Errorf("storage got after_epoch=%d limit=%d, want 3 and 10", gotAfter, gotLimit)
		}

		var page api.MlsCommitPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not an MlsCommitPage: %v", err)
		}
		if len(page.Commits) != 1 || page.Commits[0].Epoch != 4 || page.Commits[0].Message != mlsBlobB64 {
			t.Errorf("page = %+v, want the one commit as base64", page.Commits)
		}
	})

	t.Run("limit defaults to the contract's maximum", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		var gotLimit int
		store.listMlsCommits = func(_ context.Context, _ uuid.UUID, _ int64, limit int) ([]storage.MlsCommit, error) {
			gotLimit = limit
			return nil, nil
		}

		do(t, store, request(http.MethodGet,
			channelPath("/mls/commits")+"?after_epoch=0", "", withSessionCookie("tok")))
		if gotLimit != 50 {
			t.Errorf("limit defaulted to %d, want 50", gotLimit)
		}
	})

	t.Run("an empty log is an empty array", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.listMlsCommits = func(context.Context, uuid.UUID, int64, int) ([]storage.MlsCommit, error) {
			return nil, nil
		}

		rec := do(t, store, request(http.MethodGet,
			channelPath("/mls/commits")+"?after_epoch=0", "", withSessionCookie("tok")))
		if !strings.Contains(rec.Body.String(), `"commits":[]`) {
			t.Errorf("body %s, want commits as an empty array", rec.Body.String())
		}
	})

	t.Run("refuses malformed paging", func(t *testing.T) {
		t.Parallel()
		for name, query := range map[string]string{
			"negative after_epoch": "?after_epoch=-1",
			"limit zero":           "?after_epoch=0&limit=0",
			"limit over max":       "?after_epoch=0&limit=51",
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				rec := do(t, encryptedMemberStore(), request(http.MethodGet,
					channelPath("/mls/commits")+query, "", withSessionCookie("tok")))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	t.Run("a stranger gets the channel's own 404", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), encryptedChannel(), false)
		rec := do(t, store, request(http.MethodGet,
			channelPath("/mls/commits")+"?after_epoch=0", "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})
}

func TestSubmitMlsCommit(t *testing.T) {
	t.Parallel()
	commitBody := func(welcomes string) string {
		body := fmt.Sprintf(`{"epoch":0,"message":%q`, mlsBlobB64)
		if welcomes != "" {
			body += `,"welcomes":` + welcomes
		}
		return body + "}"
	}

	t.Run("an accepted commit announces the epoch and nudges the recipients", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		var got storage.NewMlsCommit
		store.submitMlsCommit = func(_ context.Context, nc storage.NewMlsCommit) (storage.MlsCommitOutcome, error) {
			got = nc
			return storage.MlsCommitOutcome{Epoch: 1, WelcomeUserIDs: []uuid.UUID{peerUUID()}}, nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPost, channelPath("/mls/commits"),
			commitBody(fmt.Sprintf(`[{"device_id":%q,"welcome":%q}]`, mlsDeviceID, mlsBlobB64)),
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if got.Epoch != 0 || string(got.Message) != "blob" {
			t.Errorf("storage got %+v, want the decoded commit at epoch 0", got)
		}
		if len(got.Welcomes) != 1 || got.Welcomes[0].DeviceID != mlsDeviceUUID() ||
			string(got.Welcomes[0].Welcome) != "blob" {
			t.Errorf("storage got welcomes %+v, want the one decoded delivery", got.Welcomes)
		}

		commits, welcomes := rt.mlsEvents()
		if len(commits) != 1 || commits[0].channelID != channelUUID() || commits[0].epoch != 1 {
			t.Errorf("mls_commit events = %+v, want one naming epoch 1", commits)
		}
		if len(welcomes) != 1 || welcomes[0] != peerUUID() {
			t.Errorf("mls_welcome events = %v, want one to the peer", welcomes)
		}
	})

	t.Run("a lost epoch race is 409", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.submitMlsCommit = func(context.Context, storage.NewMlsCommit) (storage.MlsCommitOutcome, error) {
			return storage.MlsCommitOutcome{}, storage.ErrMlsEpochConflict
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPost, channelPath("/mls/commits"),
			commitBody(""), withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusConflict, "mls_epoch_conflict")

		// And nothing was announced. An mls_commit for an epoch that was
		// refused would send every member to fetch a log that does not
		// have it.
		if commits, welcomes := rt.mlsEvents(); len(commits) != 0 || len(welcomes) != 0 {
			t.Errorf("a refused commit announced %+v / %v", commits, welcomes)
		}
	})

	t.Run("a channel with no group is 404", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.submitMlsCommit = func(context.Context, storage.NewMlsCommit) (storage.MlsCommitOutcome, error) {
			return storage.MlsCommitOutcome{}, storage.ErrMlsGroupNotFound
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/mls/commits"),
			commitBody(""), withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "mls_group_not_found")
	})

	t.Run("a welcome naming no device is 400", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.submitMlsCommit = func(context.Context, storage.NewMlsCommit) (storage.MlsCommitOutcome, error) {
			return storage.MlsCommitOutcome{}, storage.ErrMlsDeviceNotFound
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/mls/commits"),
			commitBody(fmt.Sprintf(`[{"device_id":%q,"welcome":%q}]`, mlsDeviceID, mlsBlobB64)),
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("refuses a malformed submission", func(t *testing.T) {
		t.Parallel()
		duplicate := fmt.Sprintf(`[{"device_id":%q,"welcome":%q},{"device_id":%q,"welcome":%q}]`,
			mlsDeviceID, mlsBlobB64, mlsDeviceID, mlsBlobB64)
		for name, body := range map[string]string{
			"negative epoch":       fmt.Sprintf(`{"epoch":-1,"message":%q}`, mlsBlobB64),
			"empty message":        `{"epoch":0,"message":""}`,
			"message not base64":   `{"epoch":0,"message":"not base64!"}`,
			"duplicate device ids": commitBody(duplicate),
			"welcome not base64":   commitBody(fmt.Sprintf(`[{"device_id":%q,"welcome":"not base64!"}]`, mlsDeviceID)),
			"welcome with nil device": commitBody(
				`[{"device_id":"00000000-0000-0000-0000-000000000000","welcome":"` + mlsBlobB64 + `"}]`),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				// submitMlsCommit stays unwired: reaching it is a 500.
				rec := do(t, encryptedMemberStore(),
					request(http.MethodPost, channelPath("/mls/commits"), body,
						withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	t.Run("a stranger gets the channel's own 404", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), encryptedChannel(), false)
		rec := do(t, store, request(http.MethodPost, channelPath("/mls/commits"),
			commitBody(""), withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})
}

func TestMlsWelcomes(t *testing.T) {
	t.Parallel()

	t.Run("lists every welcome for the caller's devices", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotUser uuid.UUID
		store.listMlsWelcomes = func(_ context.Context, userID uuid.UUID) ([]storage.MlsWelcome, error) {
			gotUser = userID
			return []storage.MlsWelcome{{
				ID: mlsWelcomeUUID(), ChannelID: channelUUID(), GroupID: []byte("blob"),
				DeviceID: mlsDeviceUUID(), Welcome: []byte("blob"),
				CreatedAt: time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
			}}, nil
		}

		rec := do(t, store, request(http.MethodGet, "/api/v1/users/me/mls/welcomes", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		// The caller's own, always: nothing in the request names a user.
		if gotUser != fixtureUser().ID {
			t.Errorf("listed for %s, want the session's user", gotUser)
		}
		var list api.MlsWelcomeList
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("body is not an MlsWelcomeList: %v", err)
		}
		if len(list.Welcomes) != 1 || list.Welcomes[0].Welcome != mlsBlobB64 ||
			list.Welcomes[0].GroupId != mlsBlobB64 {
			t.Errorf("welcomes = %+v, want the one welcome as base64", list.Welcomes)
		}
	})

	t.Run("an empty list is an empty array", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.listMlsWelcomes = func(context.Context, uuid.UUID) ([]storage.MlsWelcome, error) {
			return nil, nil
		}

		rec := do(t, store, request(http.MethodGet, "/api/v1/users/me/mls/welcomes", "", withSessionCookie("tok")))
		if !strings.Contains(rec.Body.String(), `"welcomes":[]`) {
			t.Errorf("body %s, want welcomes as an empty array", rec.Body.String())
		}
	})

	t.Run("acknowledgement is idempotent and scoped to the caller", func(t *testing.T) {
		t.Parallel()
		path := "/api/v1/users/me/mls/welcomes/" + mlsWelcomeID

		store := authedStore(fixtureUser())
		var gotUser, gotWelcome uuid.UUID
		store.deleteMlsWelcome = func(_ context.Context, userID, welcomeID uuid.UUID) error {
			gotUser, gotWelcome = userID, welcomeID
			return nil
		}
		rec := do(t, store, request(http.MethodDelete, path, "", withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if gotUser != fixtureUser().ID || gotWelcome != mlsWelcomeUUID() {
			t.Errorf("storage got (%s, %s), want the session's user and the path's welcome", gotUser, gotWelcome)
		}

		// Another user's welcome is 404, which the contract distinguishes
		// from the idempotent 204 above.
		store.deleteMlsWelcome = func(context.Context, uuid.UUID, uuid.UUID) error {
			return fmt.Errorf("wrapped: %w", storage.ErrMlsWelcomeNotFound)
		}
		rec = do(t, store, request(http.MethodDelete, path, "", withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "mls_welcome_not_found")

		// And an unexpected storage failure is a 500, never a quiet 204.
		store.deleteMlsWelcome = func(context.Context, uuid.UUID, uuid.UUID) error {
			return errors.New("boom")
		}
		rec = do(t, store, request(http.MethodDelete, path, "", withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusInternalServerError, "internal_error")
	})
}
