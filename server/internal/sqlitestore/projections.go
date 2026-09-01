package sqlitestore

import (
	"database/sql"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// rowScanner is the one method a scan needs, satisfied by both *sql.Row and
// *sql.Rows, so a projection can be scanned identically from a single-row
// query and from an iteration.
type rowScanner interface {
	Scan(dest ...any) error
}

// The users table has exactly TWO projections and one scan, and this is where
// all three live — the same rule storage/users.go states at length, for the
// same reason: a third copy is not a shortcut, it is the bug. A column is
// added to userColumns, memberUserColumns and userScan and nowhere else;
// adding it to one and not the others is a scan error on the first read
// rather than a wrong value that survives.

// userColumns is the canonical column list, in the order userScan expects.
// The final expression derives SsoLinked; the correlated users.id resolves
// against the row being selected or returned, so the same list works in
// SELECT ... FROM users and in RETURNING on it.
//
// password_hash is COALESCEd because an account provisioned by a directory or
// created by single sign-on has no password credential. Every reader keeps its
// plain string and gets "" for those, which Login refuses explicitly.
const userColumns = `id, username, email, display_name, COALESCE(password_hash, ''), locale, is_admin, is_active, must_change_password, created_at, updated_at,
	scim_external_id, scim_user_name,
	EXISTS (SELECT 1 FROM oidc_identities oi WHERE oi.user_id = users.id)`

// memberUserColumns is userColumns qualified (alias u), for the queries that
// join users against another table.
const memberUserColumns = `u.id, u.username, u.email, u.display_name, COALESCE(u.password_hash, ''),
	        u.locale, u.is_admin, u.is_active, u.must_change_password, u.created_at, u.updated_at,
	        u.scim_external_id, u.scim_user_name,
	        EXISTS (SELECT 1 FROM oidc_identities oi WHERE oi.user_id = u.id)`

// userScan holds one row of either projection while it is being read. The
// nullable columns land in sql.Null* first because the domain type models
// them as pointers, and SQLite hands NULL back as an untyped nil.
type userScan struct {
	u              storage.User
	email          sql.NullString
	scimExternalID sql.NullString
	scimUserName   sql.NullString
}

// targets is the scan destination list for both projections above, in their
// order. It is a method rather than an argument list written out at each call
// site so that a query returning a user ALONGSIDE other columns appends this
// list instead of retyping it.
func (us *userScan) targets() []any {
	return []any{
		&us.u.ID, &us.u.Username, &us.email, &us.u.DisplayName, &us.u.PasswordHash,
		&us.u.Locale, &us.u.IsAdmin, &us.u.IsActive, &us.u.MustChangePassword,
		timeScan{dst: &us.u.CreatedAt}, timeScan{dst: &us.u.UpdatedAt},
		&us.scimExternalID, &us.scimUserName,
		&us.u.SsoLinked,
	}
}

// user returns the scanned row with its optional columns attached.
func (us *userScan) user() storage.User {
	u := us.u
	u.Email = stringPtr(us.email)
	u.ScimExternalID = stringPtr(us.scimExternalID)
	u.ScimUserName = stringPtr(us.scimUserName)
	return u
}

// scanUser scans one userColumns (or memberUserColumns) row. sql.ErrNoRows
// becomes storage.ErrNotFound, so handlers branch on the same sentinel on
// both drivers.
func scanUser(row rowScanner) (storage.User, error) {
	var us userScan
	if err := row.Scan(us.targets()...); err != nil {
		return storage.User{}, notFound(err)
	}
	return us.user(), nil
}

// sessionColumns is the canonical column list session queries return, in the
// order scanSession expects.
const sessionColumns = `id, user_id, family_id, access_expires_at, refresh_expires_at, created_at, totp_enrollment_required`

// sessionScanTargets is the scan destination for sessionColumns, exposed the
// same way userScan.targets is: SessionUserByAccessHash returns a session
// alongside a user and appends both lists rather than retyping either.
func sessionScanTargets(s *storage.Session) []any {
	return []any{
		&s.ID, &s.UserID, &s.FamilyID,
		timeScan{dst: &s.AccessExpiresAt}, timeScan{dst: &s.RefreshExpiresAt},
		timeScan{dst: &s.CreatedAt},
		&s.TotpEnrollmentRequired,
	}
}

// scanSession scans one sessionColumns row.
func scanSession(row rowScanner) (storage.Session, error) {
	var sess storage.Session
	if err := row.Scan(sessionScanTargets(&sess)...); err != nil {
		return storage.Session{}, notFound(err)
	}
	return sess, nil
}
