package sqlitestore

import (
	"context"
	"fmt"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// orgSettingsColumns is the stored projection, in the order scanOrgSettings
// expects. The two derived counts are not among them by design.
const orgSettingsColumns = `org_name, default_locale, registration_mode, require_totp, session_lifetime_hours, sso_jit_provisioning, encryption_mode`

// countAccountsWithoutTotp counts the accounts two-step enforcement would
// affect: active users with no ACTIVATED second factor. A pending setup is
// not a second factor — the account cannot complete a two-step sign-in with
// one — so it is counted as affected.
//
// is_active is INTEGER here rather than boolean, so the bare column
// PostgreSQL uses as a predicate is written as the comparison it stands for.
const countAccountsWithoutTotp = `
	SELECT count(*) FROM users u
	WHERE u.is_active = 1
	  AND NOT EXISTS (
	      SELECT 1 FROM user_totp t
	      WHERE t.user_id = u.id AND t.activated_at IS NOT NULL
	  )`

// The two conversation totals. They say nothing about the mode, which is the
// point: neither can move when the mode does, so no statement here has to
// count against the mode it is writing.
const (
	countEncryptedConversations = `SELECT count(*) FROM channels c WHERE c.e2ee = 1`
	countPlaintextConversations = `SELECT count(*) FROM channels c WHERE c.e2ee = 0`
)

// orgSettingsProjection is what every statement here selects, in the order
// scanOrgSettings expects, so the three cannot drift apart.
const orgSettingsProjection = orgSettingsColumns +
	`, (` + countAccountsWithoutTotp + `), (` + countEncryptedConversations + `), (` + countPlaintextConversations + `)`

func scanOrgSettings(row rowScanner, out *storage.OrgSettings) error {
	return row.Scan(
		&out.OrgName, &out.DefaultLocale, &out.RegistrationMode,
		&out.RequireTotp, &out.SessionLifetimeHours, &out.SsoJitProvisioning,
		&out.EncryptionMode, &out.AccountsWithoutTotp,
		&out.EncryptedConversations, &out.PlaintextConversations,
	)
}

// OrgSettings reads the instance settings. The row is guaranteed to exist:
// migration 0009 inserts it and the table's primary-key CHECK makes "exactly
// one row" a fact rather than a convention — id INTEGER PRIMARY KEY
// CHECK (id = 1) here, where PostgreSQL pins a boolean key to true, because
// SQLite has no boolean type to pin.
func (s *Store) OrgSettings(ctx context.Context) (storage.OrgSettings, error) {
	var out storage.OrgSettings
	if err := scanOrgSettings(s.db.QueryRowContext(ctx,
		`SELECT `+orgSettingsProjection+` FROM org_settings`,
	), &out); err != nil {
		return storage.OrgSettings{}, fmt.Errorf("org settings: %w", err)
	}
	return out, nil
}

// EncryptionMode reads just the mode, which is what the conversation-creation
// paths ask for. It is a separate one-column read rather than OrgSettings
// because those paths run on every channel and direct message ever opened,
// and the two aggregate counts beside the settings row have no business in
// that budget — a scan of channels per conversation created, on this driver
// as on the other.
func (s *Store) EncryptionMode(ctx context.Context) (string, error) {
	var mode string
	if err := s.db.QueryRowContext(ctx, `SELECT encryption_mode FROM org_settings`).Scan(&mode); err != nil {
		return "", fmt.Errorf("org encryption mode: %w", err)
	}
	return mode, nil
}

// SetEncryptionMode writes the mode and returns the settings as they now
// stand.
//
// It touches one column of one row. No channel, no message and no membership
// is read or written here, which is what makes "switching modes cannot
// silently decrypt or expose history" a property of the code rather than a
// promise: there is no path from this statement to a conversation. The two
// conversation totals it answers with are the same two it would have
// answered before the write, and that is the honest thing to show.
func (s *Store) SetEncryptionMode(ctx context.Context, mode string) (storage.OrgSettings, error) {
	var out storage.OrgSettings
	if err := scanOrgSettings(s.db.QueryRowContext(ctx,
		`UPDATE org_settings SET encryption_mode = ?, updated_at = ?
		 RETURNING `+orgSettingsProjection,
		mode, s.nowText(),
	), &out); err != nil {
		return storage.OrgSettings{}, fmt.Errorf("set org encryption mode: %w", err)
	}
	return out, nil
}

// UpdateOrgSettings applies a per-field patch and returns the settings as
// they now stand. Nothing here takes effect mid-session: require_totp is
// read at the next sign-in, and session_lifetime_hours at the next mint.
//
// PostgreSQL casts each parameter (COALESCE($1::text, org_name)) because it
// must know the type of a NULL before it can resolve the function. SQLite has
// no static types to resolve, so COALESCE(?, column) with an unbound-to-NULL
// parameter yields the column value directly. What the parameters DO need is
// an explicit encoding on the way in: a *bool would otherwise reach the
// driver as a Go bool rather than the 0/1 the schema's CHECK constraints
// allow, so the two nullable non-text fields go through orgNullBool and
// orgNullInt.
func (s *Store) UpdateOrgSettings(ctx context.Context, patch storage.OrgSettingsPatch) (storage.OrgSettings, error) {
	var out storage.OrgSettings
	if err := scanOrgSettings(s.db.QueryRowContext(ctx,
		`UPDATE org_settings SET
		     org_name               = COALESCE(?, org_name),
		     default_locale         = COALESCE(?, default_locale),
		     registration_mode      = COALESCE(?, registration_mode),
		     require_totp           = COALESCE(?, require_totp),
		     session_lifetime_hours = COALESCE(?, session_lifetime_hours),
		     sso_jit_provisioning   = COALESCE(?, sso_jit_provisioning),
		     updated_at             = ?
		 RETURNING `+orgSettingsProjection,
		nullString(patch.OrgName), nullString(patch.DefaultLocale), nullString(patch.RegistrationMode),
		orgNullBool(patch.RequireTotp), orgNullInt(patch.SessionLifetimeHours),
		orgNullBool(patch.SsoJitProvisioning), s.nowText(),
	), &out); err != nil {
		return storage.OrgSettings{}, fmt.Errorf("update org settings: %w", err)
	}
	return out, nil
}

// orgNullBool binds an optional boolean as the 0/1 the schema stores; nil
// becomes SQL NULL, which is what makes a field the patch did not mention
// fall through COALESCE to the column's current value.
func orgNullBool(b *bool) any {
	if b == nil {
		return nil
	}
	return boolValue(*b)
}

// orgNullInt binds an optional integer; nil becomes SQL NULL.
func orgNullInt(n *int) any {
	if n == nil {
		return nil
	}
	return int64(*n)
}
