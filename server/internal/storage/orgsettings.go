package storage

import (
	"context"
	"fmt"
)

// OrgSettings is the instance's own configuration: the single row of
// org_settings, plus one number computed from the users table.
type OrgSettings struct {
	OrgName              string
	DefaultLocale        string
	RegistrationMode     string
	RequireTotp          bool
	SessionLifetimeHours int
	// SsoJitProvisioning is whether an identity the provider vouches for,
	// matching no account here, creates one. Off by default, and read at
	// exactly one place — the callback's resolution ladder — because while
	// it is off the account-creating branch must not run at all (migration
	// 0015).
	SsoJitProvisioning bool

	// AccountsWithoutTotp is how many accounts two-step enforcement would
	// affect. It is DERIVED on every read and stored nowhere: a cached copy
	// would go stale the moment somebody finished their setup, and the whole
	// value of the number is that the admin can trust it before flipping the
	// switch.
	AccountsWithoutTotp int
}

// OrgSettingsPatch is the settings screen's per-field save. A nil field is
// one the request did not mention and this call must not change — the design
// has no Save button, so every request carries one field.
type OrgSettingsPatch struct {
	OrgName              *string
	DefaultLocale        *string
	RegistrationMode     *string
	RequireTotp          *bool
	SessionLifetimeHours *int
	SsoJitProvisioning   *bool
}

// orgSettingsColumns is the stored projection, in the order scanning
// expects. AccountsWithoutTotp is not among them by design.
const orgSettingsColumns = `org_name, default_locale, registration_mode, require_totp, session_lifetime_hours, sso_jit_provisioning`

// countAccountsWithoutTotp counts the accounts two-step enforcement would
// affect: active users with no ACTIVATED second factor. A pending setup is
// not a second factor — the account cannot complete a two-step sign-in with
// one — so it is counted as affected.
const countAccountsWithoutTotp = `
	SELECT count(*) FROM users u
	WHERE u.is_active
	  AND NOT EXISTS (
	      SELECT 1 FROM user_totp t
	      WHERE t.user_id = u.id AND t.activated_at IS NOT NULL
	  )`

// OrgSettings reads the instance settings. The row is guaranteed to exist:
// migration 0009 inserts it and the table's primary-key CHECK makes "exactly
// one row" a fact rather than a convention.
func (s *Store) OrgSettings(ctx context.Context) (OrgSettings, error) {
	var out OrgSettings
	if err := s.pool.QueryRow(ctx,
		`SELECT `+orgSettingsColumns+`, (`+countAccountsWithoutTotp+`) FROM org_settings`,
	).Scan(
		&out.OrgName, &out.DefaultLocale, &out.RegistrationMode,
		&out.RequireTotp, &out.SessionLifetimeHours, &out.SsoJitProvisioning,
		&out.AccountsWithoutTotp,
	); err != nil {
		return OrgSettings{}, fmt.Errorf("org settings: %w", err)
	}
	return out, nil
}

// UpdateOrgSettings applies a per-field patch and returns the settings as
// they now stand. Nothing here takes effect mid-session: require_totp is
// read at the next sign-in, and session_lifetime_hours at the next mint.
func (s *Store) UpdateOrgSettings(ctx context.Context, patch OrgSettingsPatch) (OrgSettings, error) {
	var out OrgSettings
	if err := s.pool.QueryRow(ctx,
		`UPDATE org_settings SET
		     org_name               = COALESCE($1::text, org_name),
		     default_locale         = COALESCE($2::text, default_locale),
		     registration_mode      = COALESCE($3::text, registration_mode),
		     require_totp           = COALESCE($4::boolean, require_totp),
		     session_lifetime_hours = COALESCE($5::integer, session_lifetime_hours),
		     sso_jit_provisioning   = COALESCE($6::boolean, sso_jit_provisioning),
		     updated_at             = now()
		 RETURNING `+orgSettingsColumns+`, (`+countAccountsWithoutTotp+`)`,
		patch.OrgName, patch.DefaultLocale, patch.RegistrationMode,
		patch.RequireTotp, patch.SessionLifetimeHours, patch.SsoJitProvisioning,
	).Scan(
		&out.OrgName, &out.DefaultLocale, &out.RegistrationMode,
		&out.RequireTotp, &out.SessionLifetimeHours, &out.SsoJitProvisioning,
		&out.AccountsWithoutTotp,
	); err != nil {
		return OrgSettings{}, fmt.Errorf("update org settings: %w", err)
	}
	return out, nil
}
