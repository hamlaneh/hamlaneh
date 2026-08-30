package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The two organisation encryption modes (ADR 011). Strict creates every
// conversation end-to-end encrypted; Compliance creates every conversation
// server-readable so that search, retention and export can exist. The mode
// governs births only — no conversation ever changes its encryption state.
const (
	EncryptionModeStrict     = "strict"
	EncryptionModeCompliance = "compliance"
)

// OrgSettings is the instance's own configuration: the single row of
// org_settings, plus two numbers computed beside it.
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
	// EncryptionMode is what conversations created from now on are born as
	// (migration 0018). It is read here and written only by
	// SetEncryptionMode: OrgSettingsPatch deliberately carries no field for
	// it, so the field-by-field settings save cannot flip it in passing.
	EncryptionMode string

	// AccountsWithoutTotp is how many accounts two-step enforcement would
	// affect. It is DERIVED on every read and stored nowhere: a cached copy
	// would go stale the moment somebody finished their setup, and the whole
	// value of the number is that the admin can trust it before flipping the
	// switch.
	AccountsWithoutTotp int
	// ConversationsOutsideMode is how many conversations this instance holds
	// whose encryption disagrees with the current mode. Derived for the same
	// reason, and permanent rather than transitional: those conversations are
	// never converted, so this is the standing count of what the mode does
	// not describe — the number the settings screen shows instead of
	// implying the mode covers everything.
	ConversationsOutsideMode int
}

// OrgSettingsPatch is the settings screen's per-field save. A nil field is
// one the request did not mention and this call must not change — the design
// has no Save button, so every request carries one field.
//
// There is no encryption-mode field here on purpose (ADR 011): that setting
// is a decision an administrator should have to mean, and it is written only
// through SetEncryptionMode. Its absence from this struct is what makes
// "the generic settings write cannot flip the mode" structural.
type OrgSettingsPatch struct {
	OrgName              *string
	DefaultLocale        *string
	RegistrationMode     *string
	RequireTotp          *bool
	SessionLifetimeHours *int
	SsoJitProvisioning   *bool
}

// orgSettingsColumns is the stored projection, in the order scanning
// expects. The two derived counts are not among them by design.
const orgSettingsColumns = `org_name, default_locale, registration_mode, require_totp, session_lifetime_hours, sso_jit_provisioning, encryption_mode`

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

// The standing count of conversations the mode does not describe: plaintext
// ones under strict, encrypted ones under compliance. It is written twice
// because it must correlate to the row the statement is returning — the
// stored row when reading, the just-updated row when writing — and a
// statement that counted against the mode it was replacing would answer a
// mode change with the previous mode's number.
const (
	countOutsideStoredMode  = `SELECT count(*) FROM channels c WHERE c.e2ee <> (org_settings.encryption_mode = '` + EncryptionModeStrict + `')`
	countOutsideUpdatedMode = `SELECT count(*) FROM channels c WHERE c.e2ee <> (updated.encryption_mode = '` + EncryptionModeStrict + `')`
)

// scanOrgSettings fills out from the projection every statement here selects,
// in one place so the three cannot drift apart.
func scanOrgSettings(row pgx.Row, out *OrgSettings) error {
	return row.Scan(
		&out.OrgName, &out.DefaultLocale, &out.RegistrationMode,
		&out.RequireTotp, &out.SessionLifetimeHours, &out.SsoJitProvisioning,
		&out.EncryptionMode, &out.AccountsWithoutTotp, &out.ConversationsOutsideMode,
	)
}

// OrgSettings reads the instance settings. The row is guaranteed to exist:
// migration 0009 inserts it and the table's primary-key CHECK makes "exactly
// one row" a fact rather than a convention.
func (s *Store) OrgSettings(ctx context.Context) (OrgSettings, error) {
	var out OrgSettings
	if err := scanOrgSettings(s.pool.QueryRow(ctx,
		`SELECT `+orgSettingsColumns+`, (`+countAccountsWithoutTotp+`), (`+countOutsideStoredMode+`)
		 FROM org_settings`,
	), &out); err != nil {
		return OrgSettings{}, fmt.Errorf("org settings: %w", err)
	}
	return out, nil
}

// EncryptionMode reads just the mode, which is what the conversation-creation
// paths ask for. It is a separate one-column read rather than OrgSettings
// because those paths run on every channel and direct message ever opened,
// and the two aggregate counts beside the settings row have no business in
// that budget.
func (s *Store) EncryptionMode(ctx context.Context) (string, error) {
	var mode string
	if err := s.pool.QueryRow(ctx, `SELECT encryption_mode FROM org_settings`).Scan(&mode); err != nil {
		return "", fmt.Errorf("org encryption mode: %w", err)
	}
	return mode, nil
}

// SetEncryptionMode writes the mode and returns the settings as they now
// stand, counted against the mode that was just written.
//
// It touches one column of one row. No channel, no message and no membership
// is read or written here, which is what makes "switching modes cannot
// silently decrypt or expose history" a property of the code rather than a
// promise: there is no path from this statement to a conversation.
func (s *Store) SetEncryptionMode(ctx context.Context, mode string) (OrgSettings, error) {
	var out OrgSettings
	if err := scanOrgSettings(s.pool.QueryRow(ctx,
		`WITH updated AS (
		     UPDATE org_settings SET encryption_mode = $1, updated_at = now()
		     RETURNING `+orgSettingsColumns+`
		 )
		 SELECT `+orgSettingsColumns+`, (`+countAccountsWithoutTotp+`), (`+countOutsideUpdatedMode+`)
		 FROM updated`,
		mode,
	), &out); err != nil {
		return OrgSettings{}, fmt.Errorf("set org encryption mode: %w", err)
	}
	return out, nil
}

// UpdateOrgSettings applies a per-field patch and returns the settings as
// they now stand. Nothing here takes effect mid-session: require_totp is
// read at the next sign-in, and session_lifetime_hours at the next mint.
func (s *Store) UpdateOrgSettings(ctx context.Context, patch OrgSettingsPatch) (OrgSettings, error) {
	var out OrgSettings
	if err := scanOrgSettings(s.pool.QueryRow(ctx,
		`WITH updated AS (
		     UPDATE org_settings SET
		         org_name               = COALESCE($1::text, org_name),
		         default_locale         = COALESCE($2::text, default_locale),
		         registration_mode      = COALESCE($3::text, registration_mode),
		         require_totp           = COALESCE($4::boolean, require_totp),
		         session_lifetime_hours = COALESCE($5::integer, session_lifetime_hours),
		         sso_jit_provisioning   = COALESCE($6::boolean, sso_jit_provisioning),
		         updated_at             = now()
		     RETURNING `+orgSettingsColumns+`
		 )
		 SELECT `+orgSettingsColumns+`, (`+countAccountsWithoutTotp+`), (`+countOutsideUpdatedMode+`)
		 FROM updated`,
		patch.OrgName, patch.DefaultLocale, patch.RegistrationMode,
		patch.RequireTotp, patch.SessionLifetimeHours, patch.SsoJitProvisioning,
	), &out); err != nil {
		return OrgSettings{}, fmt.Errorf("update org settings: %w", err)
	}
	return out, nil
}
