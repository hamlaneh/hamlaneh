package storage_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

func TestOrgSettingsIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
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
		if settings.OrgName == "" || settings.DefaultLocale != "en" || settings.SessionLifetimeHours != 720 {
			t.Errorf("unexpected defaults: %+v", settings)
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
		})
		if err != nil {
			t.Fatalf("UpdateOrgSettings: %v", err)
		}
		want := storage.OrgSettings{
			OrgName: "Hamlaneh QA", DefaultLocale: "fa", RegistrationMode: "open",
			RequireTotp: false, SessionLifetimeHours: 24,
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
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() {
			if closeErr := conn.Close(ctx); closeErr != nil {
				t.Errorf("close: %v", closeErr)
			}
		}()

		// No column holds it. If one ever appears, this test is the place
		// the mistake shows: a stored count goes stale the moment somebody
		// finishes their two-step setup.
		var stored int
		if scanErr := conn.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns
			 WHERE table_name = 'org_settings' AND column_name = 'accounts_without_totp'`,
		).Scan(&stored); scanErr != nil {
			t.Fatalf("inspect columns: %v", scanErr)
		}
		if stored != 0 {
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
