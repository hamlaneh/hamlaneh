package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The recovery surface (ADR 010). These pin the contract semantics the storage
// tests cannot see: which status each outcome carries, which code, and that
// every one of the four acts on the SESSION's user rather than on anything a
// request named.

const mlsBackupPath = "/api/v1/users/me/mls/backup"

func TestPutMlsBackup(t *testing.T) {
	t.Parallel()

	t.Run("stores the envelope under the session's own account", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotUser uuid.UUID
		var gotEnvelope []byte
		var gotCounter int64
		store.putMlsBackup = func(_ context.Context, userID uuid.UUID, envelope []byte, counter int64) error {
			gotUser, gotEnvelope, gotCounter = userID, envelope, counter
			return nil
		}

		rec := do(t, store, request(http.MethodPut, mlsBackupPath,
			fmt.Sprintf(`{"envelope":%q,"counter":7}`, mlsBlobB64),
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		// Nothing in the request could have named another account, and this is
		// where that is a fact rather than a claim.
		if gotUser != fixtureUser().ID {
			t.Errorf("stored under %s, want the session's user %s", gotUser, fixtureUser().ID)
		}
		if string(gotEnvelope) != "blob" {
			t.Errorf("storage got %q, want the decoded envelope", gotEnvelope)
		}
		if gotCounter != 7 {
			t.Errorf("storage got counter %d, want the one uploaded", gotCounter)
		}
	})

	t.Run("a counter that does not advance is 409", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.putMlsBackup = func(context.Context, uuid.UUID, []byte, int64) error {
			return storage.ErrMlsBackupStale
		}

		rec := do(t, store, request(http.MethodPut, mlsBackupPath,
			fmt.Sprintf(`{"envelope":%q,"counter":1}`, mlsBlobB64),
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusConflict, "mls_backup_stale")
	})

	t.Run("refuses a malformed request", func(t *testing.T) {
		t.Parallel()
		// The distinction worth pinning: a counter below the contract's
		// minimum is a 400, NOT the 409 a stale write gets. Answering it as a
		// conflict would tell a client to retry with a higher number when what
		// it has is a framing bug.
		for name, body := range map[string]string{
			"counter zero":     fmt.Sprintf(`{"envelope":%q,"counter":0}`, mlsBlobB64),
			"counter negative": fmt.Sprintf(`{"envelope":%q,"counter":-1}`, mlsBlobB64),
			"envelope empty":   `{"envelope":"","counter":1}`,
			"not base64":       `{"envelope":"not base64!","counter":1}`,
			"over the cap": fmt.Sprintf(`{"envelope":%q,"counter":1}`,
				strings.Repeat("A", 700004)),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				// putMlsBackup stays unwired: reaching storage would be a 500,
				// so a 400 here also proves nothing was stored.
				rec := do(t, authedStore(fixtureUser()),
					request(http.MethodPut, mlsBackupPath, body,
						withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestGetMlsBackup(t *testing.T) {
	t.Parallel()

	t.Run("returns the stored envelope re-encoded", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		updated := time.Date(2026, 3, 3, 10, 30, 0, 0, time.UTC)
		var gotUser uuid.UUID
		store.mlsBackupByUser = func(_ context.Context, userID uuid.UUID) (storage.MlsBackup, error) {
			gotUser = userID
			return storage.MlsBackup{Envelope: []byte("blob"), Counter: 4, UpdatedAt: updated}, nil
		}

		rec := do(t, store, request(http.MethodGet, mlsBackupPath, "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotUser != fixtureUser().ID {
			t.Errorf("read %s's row, want the session's user %s", gotUser, fixtureUser().ID)
		}

		var backup api.MlsBackup
		if err := json.Unmarshal(rec.Body.Bytes(), &backup); err != nil {
			t.Fatalf("body is not an MlsBackup: %v", err)
		}
		if backup.Envelope != mlsBlobB64 || backup.Counter != 4 {
			t.Errorf("got %+v, want the stored envelope at counter 4", backup)
		}
		// The date is the only freshness signal a first restore has, so it has
		// to survive the round trip intact (ADR 010, decision 3).
		if !backup.UpdatedAt.Equal(updated) {
			t.Errorf("updated_at = %s, want %s", backup.UpdatedAt, updated)
		}
	})

	t.Run("no backup is 404 with its own code", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.mlsBackupByUser = func(context.Context, uuid.UUID) (storage.MlsBackup, error) {
			return storage.MlsBackup{}, storage.ErrMlsBackupNotFound
		}

		rec := do(t, store, request(http.MethodGet, mlsBackupPath, "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "mls_backup_not_found")
	})
}

func TestDeleteMlsBackup(t *testing.T) {
	t.Parallel()

	// Idempotent, and the handler has no branch that could make it otherwise:
	// there is no "did it exist" answer anywhere on this path, which is what
	// stops a delete doubling as a way to ask whether a backup was there.
	store := authedStore(fixtureUser())
	var calls int
	var gotUser uuid.UUID
	store.deleteMlsBackup = func(_ context.Context, userID uuid.UUID) error {
		calls++
		gotUser = userID
		return nil
	}

	for attempt := 1; attempt <= 2; attempt++ {
		rec := do(t, store, request(http.MethodDelete, mlsBackupPath, "",
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("attempt %d got status %d, want 204 (body %s)", attempt, rec.Code, rec.Body.String())
		}
	}
	if calls != 2 {
		t.Errorf("storage saw %d deletes, want one per request", calls)
	}
	if gotUser != fixtureUser().ID {
		t.Errorf("deleted %s's row, want the session's user %s", gotUser, fixtureUser().ID)
	}
}

func TestDeregisterMlsDevice(t *testing.T) {
	t.Parallel()
	path := "/api/v1/users/me/mls/devices/" + mlsDeviceID

	t.Run("drops the caller's own device", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotUser, gotDevice uuid.UUID
		store.deregisterMlsDevice = func(_ context.Context, userID, deviceID uuid.UUID) error {
			gotUser, gotDevice = userID, deviceID
			return nil
		}

		rec := do(t, store, request(http.MethodDelete, path, "",
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if gotUser != fixtureUser().ID || gotDevice != mlsDeviceUUID() {
			t.Errorf("storage got (%s, %s), want the session's user and the path's device", gotUser, gotDevice)
		}
	})

	t.Run("another account's device is the same 404 as one naming nothing", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.deregisterMlsDevice = func(context.Context, uuid.UUID, uuid.UUID) error {
			return storage.ErrMlsDeviceNotFound
		}

		rec := do(t, store, request(http.MethodDelete, path, "",
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "mls_device_not_found")
	})

	t.Run("does not touch sessions", func(t *testing.T) {
		t.Parallel()
		// The fake store's session methods stay unwired, so any call into
		// revocation would fail the request rather than pass silently — which
		// is exactly what "this is not a sign-out" has to mean in code. A 204
		// here is that assertion.
		store := authedStore(fixtureUser())
		store.deregisterMlsDevice = func(context.Context, uuid.UUID, uuid.UUID) error { return nil }

		rec := do(t, store, request(http.MethodDelete, path, "",
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
	})
}
