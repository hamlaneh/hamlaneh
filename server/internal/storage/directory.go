package storage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// DirectoryCursor is the keyset anchor for ListDirectory: the last row's
// (username, id).
type DirectoryCursor struct {
	Username string
	UserID   uuid.UUID
}

// ListDirectoryParams pages the instance directory.
type ListDirectoryParams struct {
	// Query filters over username and display name, case-insensitively.
	// Empty means no filter.
	Query string
	After *DirectoryCursor
	Limit int
}

// ListDirectory pages every user of the instance by username, for the two
// people-pickers the chat shell draws.
//
// It is not ListUsers with a different sort. ListUsers backs the admin
// dashboard and pages by (created_at, id) — newest accounts last, which is
// what an operator watching an instance fill up wants. A picker is read by
// somebody hunting for a name, so it pages by (username, id) and takes a
// filter. Two readers, two orders, two queries; folding them into one
// function with a mode flag would make every call site read the flag to know
// what it gets back.
//
// The filter is a LIKE over a lower-cased needle rather than full-text: it
// has to match the middle of a name ("ami" finding "sarah.amini"), which is
// exactly what an FTS configuration would refuse to do.
func (s *Store) ListDirectory(ctx context.Context, params ListDirectoryParams) ([]User, error) {
	var afterUsername *string
	var afterID *uuid.UUID
	if params.After != nil {
		afterUsername = &params.After.Username
		afterID = &params.After.UserID
	}

	var needle *string
	if params.Query != "" {
		// The wildcards are added here, so the parameter carries no pattern
		// syntax of its own and a user typing % or _ searches for the
		// character rather than for everything.
		pattern := "%" + escapeLike(params.Query) + "%"
		needle = &pattern
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+userColumns+`
		 FROM users
		 WHERE ($1::citext IS NULL OR (username, id) > ($1::citext, $2))
		   AND ($3::text IS NULL
		        OR username ILIKE $3 ESCAPE '\'
		        OR display_name ILIKE $3 ESCAPE '\')
		 ORDER BY username, id
		 LIMIT $4`,
		afterUsername, afterID, needle, params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0, params.Limit)
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

// escapeLike neutralises the three characters LIKE treats as syntax, so a
// filter is matched as the literal text somebody typed.
func escapeLike(in string) string {
	out := make([]rune, 0, len(in))
	for _, r := range in {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}
