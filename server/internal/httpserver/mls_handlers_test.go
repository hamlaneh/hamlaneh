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
	mlsDeviceID2 = "aaaaaaaa-0000-4000-8000-000000000002"
	mlsWelcomeID = "bbbbbbbb-0000-4000-8000-000000000001"
)

func mlsDeviceUUID() uuid.UUID  { return uuid.MustParse(mlsDeviceID) }
func mlsDeviceUUID2() uuid.UUID { return uuid.MustParse(mlsDeviceID2) }
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

	// A claim consumes a single-use package, so on a channel that can never
	// have a group it is a free pool-drain against anyone you share a
	// plaintext room with. claimMlsKeyPackages stays unwired here: reaching
	// storage would be a 500, which is what proves nothing was consumed.
	t.Run("a plaintext channel is refused", func(t *testing.T) {
		t.Parallel()
		rec := do(t, memberStore(), request(http.MethodPost, channelPath("/mls/key-package-claims"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "e2ee_not_enabled")
	})

	t.Run("a stranger to the channel is refused first", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), encryptedChannel(), false)
		rec := do(t, store, request(http.MethodPost, channelPath("/mls/key-package-claims"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	// And a stranger to a PLAINTEXT channel still gets the channel's 404,
	// not the mode refusal: membership is decided first, so the new gate
	// cannot become a way to probe which channels exist.
	t.Run("a stranger to a plaintext channel learns nothing about its mode", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodPost, channelPath("/mls/key-package-claims"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})
}

// The member-device directory (ADR 007). It is a read, so most of what these
// pin is refusal ordering and wire shape — but two of them are the reason the
// endpoint is worth testing at all: a stranger must learn nothing, and a
// member with no devices must survive the trip to JSON as an empty array
// rather than as an absence.

// memberDevicesPath is the roster read's request target.
func memberDevicesPath() string { return channelPath("/mls/member-devices") }

// memberDevicesGet builds the roster read as the fixture member.
func memberDevicesGet(query string) *http.Request {
	return request(http.MethodGet, memberDevicesPath()+query, "", withSessionCookie("tok"))
}

func TestListMlsMemberDevices(t *testing.T) {
	t.Parallel()

	t.Run("returns each member with their keys", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		var gotChannel uuid.UUID
		var gotAfter *uuid.UUID
		var gotLimit int
		store.listMlsMemberDevices = func(_ context.Context, channelID uuid.UUID, after *uuid.UUID, limit int) ([]storage.MlsMemberDevice, error) {
			gotChannel, gotAfter, gotLimit = channelID, after, limit
			return []storage.MlsMemberDevice{
				{UserID: fixtureUser().ID, SignaturePublicKeys: [][]byte{[]byte("blob")}},
				// A member who has registered nothing.
				{UserID: peerUUID(), SignaturePublicKeys: [][]byte{}},
			}, nil
		}

		rec := do(t, store, memberDevicesGet(""))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotChannel != channelUUID() || gotAfter != nil {
			t.Errorf("storage got (%s, after=%v), want the path's channel from the start", gotChannel, gotAfter)
		}
		// The contract's default is its maximum, and the handler asks for one
		// row beyond the page to learn whether another exists.
		if gotLimit != 201 {
			t.Errorf("storage got limit %d, want the 200 default plus the lookahead row", gotLimit)
		}

		var page api.MlsMemberDevicePage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not an MlsMemberDevicePage: %v", err)
		}
		if len(page.Members) != 2 {
			t.Fatalf("members = %+v, want both", page.Members)
		}
		if page.Members[0].SignaturePublicKeys[0] != mlsBlobB64 {
			t.Errorf("keys = %v, want the registered key as base64", page.Members[0].SignaturePublicKeys)
		}
		if page.NextCursor != nil {
			t.Errorf("next_cursor = %v on a short page, want absent", *page.NextCursor)
		}
	})

	// The one a future "optimization" breaks, pinned here at the wire.
	//
	// The contract makes signature_public_keys required, and a member with no
	// devices must arrive as an empty array: a JSON null is a crash in the
	// client, and being dropped from the page entirely is worse than a crash
	// — the member becomes indistinguishable from a non-member, and the
	// allow-list assembled from this body evicts their real leaves.
	t.Run("a member with no devices is an empty array, not an absence", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		store.listMlsMemberDevices = func(context.Context, uuid.UUID, *uuid.UUID, int) ([]storage.MlsMemberDevice, error) {
			return []storage.MlsMemberDevice{{UserID: peerUUID()}}, nil
		}

		rec := do(t, store, memberDevicesGet(""))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"signature_public_keys":[]`) {
			t.Errorf("body %s, want the member present with an empty key array", body)
		}
		if !strings.Contains(body, peerUUID().String()) {
			t.Errorf("body %s, want the member's own id present", body)
		}
	})

	t.Run("pages with a cursor that carries the position", func(t *testing.T) {
		t.Parallel()
		store := encryptedMemberStore()
		var gotAfter *uuid.UUID
		var gotLimit int
		store.listMlsMemberDevices = func(_ context.Context, _ uuid.UUID, after *uuid.UUID, limit int) ([]storage.MlsMemberDevice, error) {
			gotAfter, gotLimit = after, limit
			// One more than the page asked for, which is the signal there is
			// another page.
			return []storage.MlsMemberDevice{
				{UserID: fixtureUser().ID},
				{UserID: peerUUID()},
			}, nil
		}

		rec := do(t, store, memberDevicesGet("?limit=1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotLimit != 2 {
			t.Errorf("storage got limit %d, want the requested 1 plus the lookahead row", gotLimit)
		}

		var page api.MlsMemberDevicePage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not an MlsMemberDevicePage: %v", err)
		}
		if len(page.Members) != 1 || page.Members[0].UserId != fixtureUser().ID {
			t.Fatalf("members = %+v, want the page trimmed to the first", page.Members)
		}
		if page.NextCursor == nil {
			t.Fatal("next_cursor is absent; the lookahead row says another page exists, " +
				"and a client that stops here sweeps against half a roster")
		}

		// Feeding it back resumes AFTER the last member handed out, which is
		// what makes a full walk cover everybody exactly once.
		rec = do(t, store, memberDevicesGet("?limit=1&cursor="+*page.NextCursor))
		if rec.Code != http.StatusOK {
			t.Fatalf("second page got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotAfter == nil || *gotAfter != fixtureUser().ID {
			t.Errorf("storage resumed after %v, want the last member of the previous page (%s)",
				gotAfter, fixtureUser().ID)
		}
	})

	t.Run("refuses a limit outside the contract's bounds", func(t *testing.T) {
		t.Parallel()
		for name, query := range map[string]string{
			"zero":         "?limit=0",
			"negative":     "?limit=-1",
			"over the max": "?limit=201",
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				// listMlsMemberDevices stays unwired: reaching it is a 500.
				rec := do(t, encryptedMemberStore(), memberDevicesGet(query))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	// The same answer every other paged read gives, from the same helper: a
	// cursor is opaque, so the only honest advice is to start the page again.
	t.Run("refuses a malformed cursor", func(t *testing.T) {
		t.Parallel()
		rec := do(t, encryptedMemberStore(), memberDevicesGet("?cursor=not-a-cursor"))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	// A channel with no group has no tree to sweep. listMlsMemberDevices
	// stays unwired: reaching storage would be a 500.
	t.Run("a plaintext channel is refused", func(t *testing.T) {
		t.Parallel()
		rec := do(t, memberStore(), memberDevicesGet(""))
		wantError(t, rec, http.StatusBadRequest, "e2ee_not_enabled")
	})

	// The row this endpoint most needs. A stranger who could read this would
	// learn the entire roster of a channel they cannot see — every member's
	// id, and how many devices each has. The refusal is the channel's own
	// 404, and storage is never reached, so nothing was even assembled.
	t.Run("a stranger to the channel learns nothing", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), encryptedChannel(), false)
		store.listMlsMemberDevices = func(context.Context, uuid.UUID, *uuid.UUID, int) ([]storage.MlsMemberDevice, error) {
			t.Error("storage was reached for a non-member; the roster was assembled before the refusal")
			return nil, nil
		}

		rec := do(t, store, memberDevicesGet(""))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
		if body := rec.Body.String(); strings.Contains(body, peerUUID().String()) {
			t.Errorf("the refusal body %s names a member", body)
		}
	})

	// And a stranger to a PLAINTEXT channel still gets the channel's 404,
	// not the mode refusal: membership is decided first, so the mode gate
	// cannot become a way to probe which channels exist.
	t.Run("a stranger to a plaintext channel learns nothing about its mode", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, memberDevicesGet(""))
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

		// One Welcome covering two devices — the shape MLS actually
		// produces, and the one the handler has to carry through without
		// copying the blob per recipient.
		rec := doRealtime(t, store, rt, request(http.MethodPost, channelPath("/mls/commits"),
			commitBody(fmt.Sprintf(`[{"device_ids":[%q,%q],"welcome":%q}]`,
				mlsDeviceID, mlsDeviceID2, mlsBlobB64)),
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if got.Epoch != 0 || string(got.Message) != "blob" {
			t.Errorf("storage got %+v, want the decoded commit at epoch 0", got)
		}
		if len(got.Welcomes) != 1 || len(got.Welcomes[0].DeviceIDs) != 2 ||
			got.Welcomes[0].DeviceIDs[0] != mlsDeviceUUID() ||
			got.Welcomes[0].DeviceIDs[1] != mlsDeviceUUID2() ||
			string(got.Welcomes[0].Welcome) != "blob" {
			t.Errorf("storage got welcomes %+v, want one delivery naming both devices", got.Welcomes)
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
			commitBody(fmt.Sprintf(`[{"device_ids":[%q],"welcome":%q}]`, mlsDeviceID, mlsBlobB64)),
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("refuses a malformed submission", func(t *testing.T) {
		t.Parallel()
		// Duplicates ACROSS two Welcomes, not within one: the per-Welcome
		// check that looks sufficient would let this through, and it is the
		// case that would leave one device a queue of Welcomes into a single
		// group.
		duplicateAcross := fmt.Sprintf(`[{"device_ids":[%q],"welcome":%q},{"device_ids":[%q],"welcome":%q}]`,
			mlsDeviceID, mlsBlobB64, mlsDeviceID, mlsBlobB64)
		duplicateWithin := fmt.Sprintf(`[{"device_ids":[%q,%q],"welcome":%q}]`,
			mlsDeviceID, mlsDeviceID, mlsBlobB64)
		tooManyWelcomes := "[" + strings.TrimSuffix(strings.Repeat(
			fmt.Sprintf(`{"device_ids":[%q],"welcome":%q},`, mlsDeviceID, mlsBlobB64), 9), ",") + "]"
		for name, body := range map[string]string{
			"negative epoch":                         fmt.Sprintf(`{"epoch":-1,"message":%q}`, mlsBlobB64),
			"empty message":                          `{"epoch":0,"message":""}`,
			"message not base64":                     `{"epoch":0,"message":"not base64!"}`,
			"duplicate device ids across welcomes":   commitBody(duplicateAcross),
			"duplicate device ids within a welcome":  commitBody(duplicateWithin),
			"more welcomes than the contract allows": commitBody(tooManyWelcomes),
			"a welcome naming no devices": commitBody(
				fmt.Sprintf(`[{"device_ids":[],"welcome":%q}]`, mlsBlobB64)),
			"welcome not base64": commitBody(fmt.Sprintf(`[{"device_ids":[%q],"welcome":"not base64!"}]`, mlsDeviceID)),
			"welcome with nil device": commitBody(
				`[{"device_ids":["00000000-0000-0000-0000-000000000000"],"welcome":"` + mlsBlobB64 + `"}]`),
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

	t.Run("acknowledgement always answers 204, scoped to the caller", func(t *testing.T) {
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
		// The user is the session's, always: it is what scopes the delete,
		// and nothing in the request could name another.
		if gotUser != fixtureUser().ID || gotWelcome != mlsWelcomeUUID() {
			t.Errorf("storage got (%s, %s), want the session's user and the path's welcome", gotUser, gotWelcome)
		}

		// A storage failure is a 500, never a quiet 204 — the uniform answer
		// is about who owns the row, not about swallowing errors.
		store.deleteMlsWelcome = func(context.Context, uuid.UUID, uuid.UUID) error {
			return errors.New("boom")
		}
		rec = do(t, store, request(http.MethodDelete, path, "", withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusInternalServerError, "internal_error")
	})

	// The handler has no branch that could answer anything but 204 for a
	// welcome id, whoever it belongs to: there is no not-found path left to
	// take. Whether the row survives is storage's property and is pinned
	// there (TestMlsWelcomesIntegration).
	t.Run("no id answers anything but 204", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.deleteMlsWelcome = func(context.Context, uuid.UUID, uuid.UUID) error { return nil }

		for _, id := range []string{mlsWelcomeID, "00000000-0000-4000-8000-00000000dead"} {
			rec := do(t, store, request(http.MethodDelete,
				"/api/v1/users/me/mls/welcomes/"+id, "", withSessionCookie("tok"), withCSRF()))
			if rec.Code != http.StatusNoContent {
				t.Errorf("acknowledging %s got status %d, want 204", id, rec.Code)
			}
		}
	})
}
