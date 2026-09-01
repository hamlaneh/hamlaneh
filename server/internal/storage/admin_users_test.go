package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// ptr is the one-liner every optional patch field needs.
func ptr[T any](v T) *T { return &v }

// adminUser builds a NewUser fixture that is an admin from the start.
func adminUser(name string) storage.NewUser {
	nu := newUser(name)
	nu.IsAdmin = true
	return nu
}

// liveFamilies counts the session families a user can still authenticate
// with. Deactivation's promise is about these rows, not about the response
// code, so the tests below assert on this rather than on a 200.
func liveFamilies(ctx context.Context, t *testing.T, store testdb.Store, userID uuid.UUID) int {
	t.Helper()
	families, err := store.ListSessionFamilies(ctx, userID, uuid.Nil)
	if err != nil {
		t.Fatalf("ListSessionFamilies: %v", err)
	}
	return len(families)
}

func TestUpdateUserAdminIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	t.Run("new accounts start active", func(t *testing.T) {
		u := mustCreateUser(ctx, t, store, newUser("startsactive"))
		if !u.IsActive {
			t.Error("a freshly created account is not active")
		}
	})

	t.Run("deactivation revokes every session family", func(t *testing.T) {
		// One admin who is not the subject, so the last-admin rule is not
		// what is being measured here.
		mustCreateUser(ctx, t, store, adminUser("keeperone"))
		victim := mustCreateUser(ctx, t, store, newUser("leaver"))
		mustCreateSession(ctx, t, store, victim.ID, tokensFor("leaver-a1", "leaver-r1"))
		mustCreateSession(ctx, t, store, victim.ID, tokensFor("leaver-a2", "leaver-r2"))

		if got := liveFamilies(ctx, t, store, victim.ID); got != 2 {
			t.Fatalf("live families before deactivation = %d, want 2", got)
		}

		updated, err := store.UpdateUserAdmin(ctx, victim.ID,
			storage.AdminUserUpdate{IsActive: ptr(false)})
		if err != nil {
			t.Fatalf("UpdateUserAdmin: %v", err)
		}
		if updated.IsActive {
			t.Error("account still reports active after deactivation")
		}
		if got := liveFamilies(ctx, t, store, victim.ID); got != 0 {
			t.Errorf("live families after deactivation = %d, want 0", got)
		}
		// The access token must stop authenticating, which is the property
		// the family count is standing in for.
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("leaver-a1")); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("access token still resolves after deactivation: %v", err)
		}
	})

	t.Run("reactivation restores sign-in and nothing else", func(t *testing.T) {
		mustCreateUser(ctx, t, store, adminUser("keepertwo"))
		u := mustCreateUser(ctx, t, store, newUser("returner"))
		mustCreateSession(ctx, t, store, u.ID, tokensFor("returner-a1", "returner-r1"))

		if _, err := store.UpdateUserAdmin(ctx, u.ID,
			storage.AdminUserUpdate{IsActive: ptr(false)}); err != nil {
			t.Fatalf("deactivate: %v", err)
		}
		back, err := store.UpdateUserAdmin(ctx, u.ID,
			storage.AdminUserUpdate{IsActive: ptr(true)})
		if err != nil {
			t.Fatalf("reactivate: %v", err)
		}
		if !back.IsActive {
			t.Error("account is not active after reactivation")
		}
		if got := liveFamilies(ctx, t, store, u.ID); got != 0 {
			t.Errorf("reactivation resurrected %d session families; it must restore only the ability to sign in", got)
		}
	})

	t.Run("a forced password reset leaves the session alive", func(t *testing.T) {
		u := mustCreateUser(ctx, t, store, newUser("forgetful"))
		mustCreateSession(ctx, t, store, u.ID, tokensFor("forgetful-a1", "forgetful-r1"))

		updated, err := store.SetTemporaryPassword(ctx, u.ID, "fake-hash-temporary")
		if err != nil {
			t.Fatalf("SetTemporaryPassword: %v", err)
		}
		if !updated.MustChangePassword {
			t.Error("must_change_password is not set after a forced reset")
		}
		if updated.PasswordHash != "fake-hash-temporary" {
			t.Errorf("password hash = %q, want the new one", updated.PasswordHash)
		}
		if got := liveFamilies(ctx, t, store, u.ID); got != 1 {
			t.Errorf("live families after a forced reset = %d, want 1 — the reset is the unlock path, not the offboarding path", got)
		}
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("forgetful-a1")); err != nil {
			t.Errorf("the user's session stopped authenticating after a forced reset: %v", err)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		if _, err := store.UpdateUserAdmin(ctx, uuid.New(),
			storage.AdminUserUpdate{IsAdmin: ptr(true)}); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("UpdateUserAdmin on an unknown id = %v, want ErrNotFound", err)
		}
		if _, err := store.SetTemporaryPassword(ctx, uuid.New(), "fake-hash"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("SetTemporaryPassword on an unknown id = %v, want ErrNotFound", err)
		}
	})
}

// TestLastAdminRuleIntegration runs on its own database: the rule counts
// every admin on the instance, so a fixture from another test would change
// the answer.
func TestLastAdminRuleIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	only := mustCreateUser(ctx, t, store, adminUser("onlyadmin"))

	t.Run("demoting the only admin is refused", func(t *testing.T) {
		_, err := store.UpdateUserAdmin(ctx, only.ID, storage.AdminUserUpdate{IsAdmin: ptr(false)})
		if !errors.Is(err, storage.ErrLastAdmin) {
			t.Fatalf("demoting the only admin = %v, want ErrLastAdmin", err)
		}
		after, err := store.UserByID(ctx, only.ID)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if !after.IsAdmin {
			t.Error("the refusal did not roll back: the account is no longer an admin")
		}
	})

	t.Run("deactivating the only admin is refused too", func(t *testing.T) {
		// Deactivation removes an admin from the set that can sign in, which
		// is the same loss the demotion above is refused for.
		_, err := store.UpdateUserAdmin(ctx, only.ID, storage.AdminUserUpdate{IsActive: ptr(false)})
		if !errors.Is(err, storage.ErrLastAdmin) {
			t.Fatalf("deactivating the only admin = %v, want ErrLastAdmin", err)
		}
		after, err := store.UserByID(ctx, only.ID)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if !after.IsActive {
			t.Error("the refusal did not roll back: the only admin is deactivated")
		}
	})

	t.Run("a deactivated admin does not count", func(t *testing.T) {
		spare := mustCreateUser(ctx, t, store, adminUser("spareadmin"))
		if _, err := store.UpdateUserAdmin(ctx, spare.ID,
			storage.AdminUserUpdate{IsActive: ptr(false)}); err != nil {
			t.Fatalf("deactivate the spare admin: %v", err)
		}
		// Two admin rows exist now, but only one of them can sign in.
		if _, err := store.UpdateUserAdmin(ctx, only.ID,
			storage.AdminUserUpdate{IsAdmin: ptr(false)}); !errors.Is(err, storage.ErrLastAdmin) {
			t.Errorf("demoting the last SIGNED-IN-ABLE admin = %v, want ErrLastAdmin", err)
		}
		if _, err := store.UpdateUserAdmin(ctx, spare.ID,
			storage.AdminUserUpdate{IsActive: ptr(true)}); err != nil {
			t.Fatalf("restore the spare admin: %v", err)
		}
	})
}

// TestLastAdminRuleIsRaceSafe is the case a per-row lock cannot cover: two
// admins demoting each other at the same instant. Each transaction reads a
// world that still contains the other, so without the instance-wide lock
// both commit and the instance is left with nobody who can administer it.
//
// The assertion is deliberately about the END STATE, not about which of the
// two was refused: either outcome is correct, and zero admins is the only
// one that is not.
func TestLastAdminRuleIsRaceSafe(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	first := mustCreateUser(ctx, t, store, adminUser("racerone"))
	second := mustCreateUser(ctx, t, store, adminUser("racertwo"))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, target := range []uuid.UUID{first.ID, second.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = store.UpdateUserAdmin(ctx, target,
				storage.AdminUserUpdate{IsAdmin: ptr(false)})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil && !errors.Is(err, storage.ErrLastAdmin) {
			t.Fatalf("demotion %d failed for an unexpected reason: %v", i, err)
		}
	}

	remaining := 0
	for _, id := range []uuid.UUID{first.ID, second.ID} {
		u, err := store.UserByID(ctx, id)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if u.IsAdmin && u.IsActive {
			remaining++
		}
	}
	if remaining != 1 {
		t.Errorf("admins left after two simultaneous demotions = %d, want exactly 1", remaining)
	}
}
