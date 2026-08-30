package sqlitestore

import (
	"database/sql"
	"errors"
	"strings"

	"modernc.org/sqlite"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// SQLite extended result codes for the constraint failures this package
// classifies. They are spelled out rather than imported because
// modernc.org/sqlite's generated constant package is an implementation
// detail of the translation, not a stable API.
//
// The PostgreSQL driver reads pgerrcode plus a constraint NAME; SQLite gives
// an extended code plus the offending column list in the message text
// ("UNIQUE constraint failed: users.username"). Both identify the same
// conflict; only the spelling differs.
const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
	sqliteConstraintForeignKey = 787
	sqliteConstraintCheck      = 275
	// A BEFORE trigger's RAISE(ABORT, …). Migration 0017 states the
	// both-or-neither MLS rule that way because SQLite cannot add a CHECK to
	// an existing table, so this code carries a CHECK violation there.
	sqliteConstraintTrigger = 1811
)

// sqliteCode returns the extended result code of err, and false when err is
// not a SQLite error at all.
func sqliteCode(err error) (int, bool) {
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return 0, false
	}
	return sqlErr.Code(), true
}

// isUniqueViolation reports whether err is a uniqueness conflict. SQLite
// raises a distinct code when the violated index is the primary key, and
// callers that upsert or race care about the conflict, never about which of
// the two indexes carried it.
func isUniqueViolation(err error) bool {
	code, ok := sqliteCode(err)
	return ok && (code == sqliteConstraintUnique || code == sqliteConstraintPrimaryKey)
}

// isForeignKeyViolation reports whether err is a foreign-key failure — a
// referenced row that does not exist, or a RESTRICT that refused a delete.
func isForeignKeyViolation(err error) bool {
	code, ok := sqliteCode(err)
	return ok && code == sqliteConstraintForeignKey
}

// isCheckViolation reports whether err is a CHECK constraint failure,
// including the trigger that stands in for the one CHECK migration 0017
// could not add.
func isCheckViolation(err error) bool {
	code, ok := sqliteCode(err)
	return ok && (code == sqliteConstraintCheck || code == sqliteConstraintTrigger)
}

// conflictColumns returns the qualified columns a uniqueness failure names,
// lowercased ("users.username"). SQLite lists every column of the violated
// index, so a composite index yields more than one.
//
// It returns nil for anything that is not a uniqueness failure, so a caller
// can switch on the result without checking the code twice.
func conflictColumns(err error) []string {
	if !isUniqueViolation(err) {
		return nil
	}
	// The whole message reads
	//
	//	constraint failed: UNIQUE constraint failed: t.a, t.b (2067)
	//
	// so the column list is what lies between the LAST "constraint failed:"
	// and the trailing result code the driver appends. Cutting at the first
	// marker or keeping the tail would put "UNIQUE" or "(2067)" in the list,
	// and a mapping that silently matched nothing would turn a 409 into a 500.
	const marker = "constraint failed:"
	msg := err.Error()
	at := strings.LastIndex(msg, marker)
	if at < 0 {
		return nil
	}
	list := strings.TrimSpace(msg[at+len(marker):])
	if strings.HasSuffix(list, ")") {
		if open := strings.LastIndex(list, "("); open >= 0 {
			list = strings.TrimSpace(list[:open])
		}
	}

	parts := strings.Split(list, ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		if c := strings.ToLower(strings.TrimSpace(p)); c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

// conflictsOn reports whether a uniqueness failure names the given qualified
// column.
func conflictsOn(err error, column string) bool {
	for _, c := range conflictColumns(err) {
		if c == column {
			return true
		}
	}
	return false
}

// mapUserConflict translates uniqueness violations on the users table into
// the sentinels handlers turn into 409s — the SQLite counterpart of
// storage.mapUserConflict, which switches on PostgreSQL constraint names.
func mapUserConflict(err error) error {
	switch {
	case conflictsOn(err, "users.username"):
		return storage.ErrUsernameTaken
	case conflictsOn(err, "users.email"):
		return storage.ErrEmailTaken
	case conflictsOn(err, "users.scim_user_name"), conflictsOn(err, "users.scim_external_id"):
		// One sentinel for both, exactly as the PostgreSQL driver does: SCIM
		// answers both with one 409 uniqueness, while the local username
		// conflict stays separate because it is the one a caller resolves by
		// retrying with a different derivation.
		return storage.ErrScimIdentifierTaken
	default:
		return err
	}
}

// notFound maps a no-rows result to storage.ErrNotFound and leaves every
// other error alone. Both drivers hand handlers the same sentinel, which is
// what lets the HTTP layer stay dialect-blind.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	return err
}
