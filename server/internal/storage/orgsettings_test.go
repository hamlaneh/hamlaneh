package storage_test

import (
	"context"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// columnCount reports how many columns of one table carry a name. The
// question is about the schema rather than about PostgreSQL, so each driver
// answers it out of its own catalogue: information_schema on one side, the
// table_info pragma on the other (ADR 012, decision 3).
func columnCount(ctx context.Context, t *testing.T, raw *testdb.Raw, table, column string) int {
	t.Helper()

	query := `SELECT count(*) FROM information_schema.columns
	          WHERE table_schema = 'public' AND table_name = ? AND column_name = ?`
	if raw.Driver() == testdb.DriverSQLite {
		query = `SELECT count(*) FROM sqlite_master m JOIN pragma_table_info(m.name) p
		         WHERE m.type = 'table' AND m.name = ? AND p.name = ?`
	}

	var n int
	if err := raw.QueryRow(ctx, query, table, column).Scan(&n); err != nil {
		t.Fatalf("inspect %s.%s: %v", table, column, err)
	}
	return n
}

func TestOrgSettingsIntegration(t *testing.T) {
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()

	t.Run("a fresh instance is closed by default", func(t *testing.T) {
		settings, err := store.OrgSettings(ctx)
		if err != nil {
			t.Fatalf("OrgSettings: %v", err)
		}
		if settings.RegistrationMode != "invite" {
			t.Errorf("registration_mode = %q, want invite — registration is closed unless somebody opens it", settings.RegistrationMode)
		}
		if settings.RequireTotp {
			t.Error("require_totp defaults on")
		}
		if settings.SsoJitProvisioning {
			t.Error("sso_jit_provisioning defaults on — the widest door in the product must be shut on a fresh instance")
		}
		if settings.OrgName == "" || settings.DefaultLocale != "en" || settings.SessionLifetimeHours != 720 {
			t.Errorf("unexpected defaults: %+v", settings)
		}
		// Strict is this product's secure posture, so it is what both
		// populations come up in — a fresh install and an instance migrated
		// from Phase 2 (ADR 011 decision 3).
		if settings.EncryptionMode != storage.EncryptionModeStrict {
			t.Errorf("encryption_mode = %q, want strict on a fresh instance", settings.EncryptionMode)
		}
	})

	t.Run("each field saves on its own", func(t *testing.T) {
		if _, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{
			OrgName: ptr("Nest"),
		}); err != nil {
			t.Fatalf("save org_name: %v", err)
		}
		after, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{
			RequireTotp: ptr(true),
		})
		if err != nil {
			t.Fatalf("save require_totp: %v", err)
		}
		if after.OrgName != "Nest" {
			t.Errorf("org_name = %q; the second save overwrote the first", after.OrgName)
		}
		if !after.RequireTotp {
			t.Error("require_totp did not save")
		}
		jit, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{
			SsoJitProvisioning: ptr(true),
		})
		if err != nil {
			t.Fatalf("save sso_jit_provisioning: %v", err)
		}
		if !jit.SsoJitProvisioning || jit.OrgName != "Nest" || !jit.RequireTotp {
			t.Errorf("sso_jit_provisioning save disturbed its neighbours: %+v", jit)
		}

		// An empty patch changes nothing at all.
		unchanged, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{})
		if err != nil {
			t.Fatalf("empty patch: %v", err)
		}
		if unchanged.OrgName != "Nest" || !unchanged.RequireTotp {
			t.Errorf("an empty patch changed something: %+v", unchanged)
		}
	})

	t.Run("every field round-trips", func(t *testing.T) {
		got, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{
			OrgName:              ptr("Hamlaneh QA"),
			DefaultLocale:        ptr("fa"),
			RegistrationMode:     ptr("open"),
			RequireTotp:          ptr(false),
			SessionLifetimeHours: ptr(24),
			// The subtest above left this on, so writing false here is a
			// real change: a column the UPDATE forgot would come back true.
			SsoJitProvisioning: ptr(false),
		})
		if err != nil {
			t.Fatalf("UpdateOrgSettings: %v", err)
		}
		want := storage.OrgSettings{
			OrgName: "Hamlaneh QA", DefaultLocale: "fa", RegistrationMode: "open",
			RequireTotp: false, SessionLifetimeHours: 24, SsoJitProvisioning: false,
			EncryptionMode:      storage.EncryptionModeStrict,
			AccountsWithoutTotp: got.AccountsWithoutTotp,
		}
		if got != want {
			t.Errorf("settings = %+v, want %+v", got, want)
		}
		reread, err := store.OrgSettings(ctx)
		if err != nil {
			t.Fatalf("OrgSettings: %v", err)
		}
		if reread != got {
			t.Errorf("re-read = %+v, want %+v", reread, got)
		}
	})

	t.Run("accounts_without_totp is computed, not stored", func(t *testing.T) {
		// No column holds it. If one ever appears, this test is the place
		// the mistake shows: a stored count goes stale the moment somebody
		// finishes their two-step setup.
		if columnCount(ctx, t, raw, "org_settings", "accounts_without_totp") != 0 {
			t.Error("accounts_without_totp is a stored column; it must be derived on every read")
		}

		base, err := store.OrgSettings(ctx)
		if err != nil {
			t.Fatalf("OrgSettings: %v", err)
		}

		// Creating an account moves the number, without anything writing it.
		fresh := mustCreateUser(ctx, t, store, newUser("nofactor"))
		after, err := store.OrgSettings(ctx)
		if err != nil {
			t.Fatalf("OrgSettings: %v", err)
		}
		if after.AccountsWithoutTotp != base.AccountsWithoutTotp+1 {
			t.Errorf("accounts_without_totp = %d after adding one account, want %d",
				after.AccountsWithoutTotp, base.AccountsWithoutTotp+1)
		}

		// A deactivated account is not one enforcement can affect: it cannot
		// sign in, so it never reaches the check.
		mustCreateUser(ctx, t, store, adminUser("settingsadmin"))
		if _, deactivateErr := store.UpdateUserAdmin(ctx, fresh.ID,
			storage.AdminUserUpdate{IsActive: ptr(false)}); deactivateErr != nil {
			t.Fatalf("deactivate: %v", deactivateErr)
		}
		back, err := store.OrgSettings(ctx)
		if err != nil {
			t.Fatalf("OrgSettings: %v", err)
		}
		if back.AccountsWithoutTotp != base.AccountsWithoutTotp+1 {
			t.Errorf("accounts_without_totp = %d after deactivating one and adding an admin, want %d",
				back.AccountsWithoutTotp, base.AccountsWithoutTotp+1)
		}
	})
}
