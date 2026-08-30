package sqlitestore

import (
	"strings"

	"modernc.org/sqlite"
)

// citextCollation is the name the migration tree declares on every column
// PostgreSQL declares citext: users.username, users.email, users.scim_user_name,
// channels.slug and oidc_identities.email_at_link.
const citextCollation = "CITEXT"

// init registers the collation that stands in for PostgreSQL's citext.
//
// SQLite ships NOCASE, and it folds ASCII only — which would make
// case-insensitivity depend on which characters an identifier happened to
// contain, and would differ from the PostgreSQL driver for anything outside
// ASCII (an email address with a non-ASCII local part, most obviously). So
// the fold is defined in Go instead: identical on every operating system,
// versioned with this code, and pinned to the PostgreSQL driver by a parity
// test over the contract's username and email alphabet.
//
// Registration is process-global and must happen before the first connection
// is opened, which is what init is for. The collation governs comparison,
// ORDER BY and the UNIQUE indexes on those columns alike — the same three
// jobs citext does in the PostgreSQL schema.
func init() {
	sqlite.MustRegisterCollationUtf8(citextCollation, compareCaseInsensitive)
}

// compareCaseInsensitive orders two values the way citext does: by their
// lowercased form. strings.ToLower is Unicode-aware and deterministic, which
// is the whole reason it is here rather than a byte-wise fold.
//
// Equality is what the UNIQUE indexes rest on, so this must return 0 for
// exactly the pairs PostgreSQL's citext calls equal. Ordering matters too —
// ListDirectory pages by username — and lowercasing before comparing is what
// citext does there as well.
func compareCaseInsensitive(left, right string) int {
	return strings.Compare(strings.ToLower(left), strings.ToLower(right))
}
