package sqlitestore

import (
	"context"
	"fmt"
	"strings"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// ListDirectory pages every user of the instance by username, for the two
// people-pickers the chat shell draws.
//
// It is not ListUsers with a different sort. ListUsers backs the admin
// dashboard and pages by (created_at, id) — newest accounts last, which is
// what an operator watching an instance fill up wants. A picker is read by
// somebody hunting for a name, so it pages by (username, id) and takes a
// filter. Two readers, two orders, two queries.
//
// The filter is a LIKE over a wildcarded needle rather than full-text: it has
// to match the middle of a name ("ami" finding "sarah.amini"), which is
// exactly what an FTS configuration would refuse to do.
//
// # The one behavioural difference from the PostgreSQL driver
//
// PostgreSQL matches with ILIKE, which folds case over the whole Unicode
// range. SQLite's LIKE folds ASCII only, and no collation changes that: LIKE
// does not consult the operands' collation at all, so the CITEXT collation
// that governs username comparison and ordering has no say in the filter.
// Both shipped locales are unaffected — English is ASCII, and Persian script
// is caseless — but a display name in a cased non-ASCII script (Cyrillic,
// Greek, accented Latin) matches case-sensitively here and case-insensitively
// on PostgreSQL. The upgrade path is a Go-side scalar function registered
// beside the CITEXT collation, at the cost of a per-row callback across the
// scan; it is not worth it until a home instance has a directory that both
// spans such a script and is too large to eyeball.
//
// The clauses are assembled rather than written as one static statement with
// IS NULL guards, for the reason ListUsers gives: a disjunction over a bound
// parameter costs the ordered range scan on username, and the unfiltered
// first page is the common read.
func (s *Store) ListDirectory(ctx context.Context, params storage.ListDirectoryParams) ([]storage.User, error) {
	var clauses []string
	var args []any

	if params.After != nil {
		// The PostgreSQL row-value comparison (username, id) > ($1, $2)
		// expanded; SQLite has no row values. username carries the CITEXT
		// collation, so both halves compare case-insensitively and agree with
		// the ORDER BY below — which is what keeps the keyset stable.
		clauses = append(clauses, `(username > ? OR (username = ? AND id > ?))`)
		args = append(args, params.After.Username, params.After.Username, params.After.UserID)
	}

	if params.Query != "" {
		// The wildcards are added here, so the parameter carries no pattern
		// syntax of its own and a user typing % or _ searches for the
		// character rather than for everything.
		pattern := "%" + escapeDirectoryLike(params.Query) + "%"
		clauses = append(clauses, `(username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, "\n			   AND ")
	}
	args = append(args, params.Limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+`
		 FROM users
		 `+where+`
		 ORDER BY username, id
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	defer rows.Close()

	users := make([]storage.User, 0, params.Limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("list directory: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	return users, nil
}

// escapeDirectoryLike neutralises the three characters LIKE treats as syntax,
// so a filter is matched as the literal text somebody typed. It is the
// counterpart of storage.escapeLike, and the escape character it produces is
// the one the ESCAPE clause above declares.
func escapeDirectoryLike(in string) string {
	out := make([]rune, 0, len(in))
	for _, r := range in {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}
