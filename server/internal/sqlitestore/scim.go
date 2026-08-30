package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// SCIM provisioning storage (docs/api/scim.md): the credentials an identity
// provider's sync engine authenticates with, and the account reads and
// writes it makes.
//
// Nothing in this file can change is_admin or is_active. Roles stay a
// deliberate act in the dashboard, so a compromised sync token cannot mint
// an administrator (§2), and deactivation goes through UpdateUserAdmin —
// which owns the last-administrator rule and the session revocation. One
// kill path, not two (§5).

// scimTokenColumns is the projection every token query selects, in the order
// scanScimToken expects.
const scimTokenColumns = `t.id, t.note, t.created_at, t.last_used_at, u.id, u.username, u.display_name`

// CreateScimToken stores one provisioning credential and returns it.
// tokenHash is the SHA-256 digest of the generated token; the raw value
// never reaches the database.
//
// PostgreSQL writes this as one data-modifying CTE — INSERT ... RETURNING
// feeding a join against users. SQLite has no data-modifying CTEs, so the
// insert and the read-back are two statements in one write transaction:
// nothing can come between them, because this transaction holds the
// database's write lock from BEGIN.
func (s *Store) CreateScimToken(ctx context.Context, createdBy uuid.UUID, tokenHash []byte, note string) (storage.ScimToken, error) {
	var (
		id  = uuid.New()
		tok storage.ScimToken
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scim_tokens (id, token_hash, created_by, note, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			id, tokenHash, createdBy, note, s.nowText(),
		); err != nil {
			return err
		}
		var scanErr error
		tok, scanErr = scanScimToken(tx.QueryRowContext(ctx,
			`SELECT `+scimTokenColumns+`
			 FROM scim_tokens t JOIN users u ON u.id = t.created_by
			 WHERE t.id = ?`,
			id,
		))
		return scanErr
	})
	if err != nil {
		return storage.ScimToken{}, fmt.Errorf("create scim token: %w", err)
	}
	return tok, nil
}

// ListScimTokens returns every token that has not been revoked, newest
// first. Revoked tokens leave the list because the table's question is which
// credentials are live; the audit log is where the history lives.
//
// The list is unpaginated on purpose: an instance has a handful of these,
// one per system it provisions from, and the contract's ScimTokenPage
// carries no cursor.
func (s *Store) ListScimTokens(ctx context.Context) ([]storage.ScimToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scimTokenColumns+`
		 FROM scim_tokens t JOIN users u ON u.id = t.created_by
		 WHERE t.revoked_at IS NULL
		 ORDER BY t.created_at DESC, t.id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list scim tokens: %w", err)
	}
	// See ListChannelsForUser on why the close error is discarded here.
	defer func() { _ = rows.Close() }()

	tokens := []storage.ScimToken{}
	for rows.Next() {
		tok, scanErr := scanScimToken(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list scim tokens: %w", scanErr)
		}
		tokens = append(tokens, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list scim tokens: %w", err)
	}
	return tokens, nil
}

// RevokeScimToken kills one provisioning credential. It reports ErrNotFound
// when no live token had that id — unlike invitation revocation, the
// contract reserves a 404 here, and an id that names nothing is worth
// telling an administrator about: it means the credential they meant to cut
// off is not the one they named.
func (s *Store) RevokeScimToken(ctx context.Context, id uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE scim_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		s.nowText(), id,
	)
	if err != nil {
		return fmt.Errorf("revoke scim token: %w", err)
	}
	n, err := rowsAffected(res)
	if err != nil {
		return fmt.Errorf("revoke scim token: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("revoke scim token: %w", storage.ErrNotFound)
	}
	return nil
}

// scimTokenTouchInterval is how stale last_used_at may get before a use
// writes it again.
//
// The column exists to tell a configured credential from one that was minted
// and forgotten, and that question does not need second-level precision.
// PostgreSQL's reason for the throttle is a dead tuple per write; SQLite has
// no MVCC to leave litter, but the throttle is kept because on this driver
// the write is what takes the database's write lock, and a provider's full
// sync is hundreds of reads.
const scimTokenTouchInterval = time.Minute

// ScimTokenByHash resolves a presented bearer token to the id of the live
// credential behind it, recording the use unless it was already recorded
// within scimTokenTouchInterval.
//
// storage.ScimTokenByHash is one statement: a CTE resolves the credential
// and an UPDATE runs against that result, so the id comes back whether or
// not the timestamp moved — making the UPDATE itself conditional would have
// returned no row on the requests it skipped, which is a 401 for a perfectly
// good token. SQLite has no data-modifying CTEs, so it is two statements in
// one write transaction, with the same property spelled out in Go: the
// lookup decides the answer, and the touch is a separate act that may not
// happen.
//
// Unknown, revoked and never-issued digests are one answer — ErrNotFound —
// because they are one answer at the door: 401, with nothing said about
// which of the three it was.
//
// One cost is real and is worth naming: every resolve opens a write
// transaction here, so it takes the write lock even on the requests whose
// touch is skipped, where PostgreSQL would have run a read-only statement.
// A home instance provisions from at most a handful of directories, so the
// contention ceiling is far above what this can reach.
func (s *Store) ScimTokenByHash(ctx context.Context, tokenHash []byte) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var lastUsedAt *time.Time
		err := tx.QueryRowContext(ctx,
			`SELECT id, last_used_at FROM scim_tokens
			 WHERE token_hash = ? AND revoked_at IS NULL`,
			tokenHash,
		).Scan(&id, nullTimeScan{dst: &lastUsedAt})
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrNotFound
		}
		if err != nil {
			return err
		}

		now := s.clock()
		if lastUsedAt != nil && lastUsedAt.After(now.Add(-scimTokenTouchInterval)) {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE scim_tokens SET last_used_at = ? WHERE id = ?`,
			asTime(now), id,
		)
		return err
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("scim token by hash: %w", err)
	}
	return id, nil
}

// CreateScimUser inserts a directory-provisioned account and returns the
// stored row. A local username collision maps to ErrUsernameTaken, which the
// caller resolves by deriving a suffixed one; a userName, externalId or
// email already held by somebody else maps to ErrScimIdentifierTaken or
// ErrEmailTaken, which are the 409 the provider must see.
//
// password_hash is absent from the column list, so it stays NULL: a
// provisioned account has no password credential, and the canonical
// projection reads that back as "" exactly as on PostgreSQL.
func (s *Store) CreateScimUser(ctx context.Context, nu storage.NewScimUser) (storage.User, error) {
	now := s.clock()
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`INSERT INTO users (id, username, email, display_name, locale, is_active, scim_user_name, scim_external_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING `+userColumns,
		uuid.New(), nu.Username, nullString(nu.Email), nu.DisplayName, nu.Locale,
		boolValue(nu.IsActive), nu.ScimUserName, nullString(nu.ExternalID),
		asTime(now), asTime(now),
	))
	if err != nil {
		return storage.User{}, fmt.Errorf("create scim user: %w", mapUserConflict(err))
	}
	return u, nil
}

// ReplaceScimUser writes the mapped attributes of one account and returns
// the stored row, or ErrNotFound when the id names nobody.
//
// is_admin and is_active are absent from the UPDATE by construction, which
// is what makes "SCIM cannot mint an administrator" a property of this
// statement rather than of a check somebody has to remember.
func (s *Store) ReplaceScimUser(ctx context.Context, id uuid.UUID, attrs storage.ScimUserAttributes) (storage.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`UPDATE users
		 SET scim_user_name    = ?,
		     scim_external_id  = ?,
		     email             = ?,
		     display_name      = ?,
		     updated_at        = ?
		 WHERE id = ?
		 RETURNING `+userColumns,
		attrs.ScimUserName, nullString(attrs.ExternalID), nullString(attrs.Email),
		attrs.DisplayName, s.nowText(), id,
	))
	if err != nil {
		return storage.User{}, fmt.Errorf("replace scim user: %w", mapUserConflict(err))
	}
	return u, nil
}

// scimUserFilter is the whole of the filter language §2 supports, written
// once and used by both statements of ListScimUsers so the page and the
// total can never be counting different things.
//
// PostgreSQL casts the parameter to citext, which makes the comparison
// case-insensitive because citext's own equality operator is. Here the two
// columns are declared COLLATE CITEXT (migration 0001 and 0014) and the
// comparison names that collation explicitly. Naming it is not redundant:
// it says at the query which of the two comparisons is meant, and it is the
// difference between matching the PostgreSQL driver and matching SQLite's
// built-in LIKE/NOCASE, which folds ASCII only — "Élodie@example.com" would
// not match "élodie@example.com" under NOCASE, and does under CITEXT
// (collation.go). The scan alternative — folding in Go — was not taken
// because these predicates must also use the unique indexes on the two
// columns, which are built in the same collation.
//
// scim_external_id is compared without a collation, matching the ::text cast
// on the other driver: a provider's own identifier is opaque and exact.
const scimUserFilter = `WHERE (? IS NULL OR scim_user_name = ? COLLATE CITEXT OR email = ? COLLATE CITEXT)
	                      AND (? IS NULL OR scim_external_id = ?)`

// ListScimUsers returns one page of accounts plus the total the filter
// matches, in stable (created_at, id) order. offset is zero-based; a limit
// of zero returns the total and no rows, which is what a SCIM count=0 asks
// for.
func (s *Store) ListScimUsers(ctx context.Context, f storage.ScimUserFilter, offset, limit int) ([]storage.User, int, error) {
	var (
		userName   = nullString(f.UserName)
		externalID = nullString(f.ExternalID)
	)

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users `+scimUserFilter,
		userName, userName, userName, externalID, externalID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count scim users: %w", err)
	}
	if limit <= 0 {
		return []storage.User{}, total, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users `+scimUserFilter+`
		 ORDER BY created_at, id
		 LIMIT ? OFFSET ?`,
		userName, userName, userName, externalID, externalID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list scim users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	users := []storage.User{}
	for rows.Next() {
		u, scanErr := scanUser(rows)
		if scanErr != nil {
			return nil, 0, fmt.Errorf("list scim users: %w", scanErr)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list scim users: %w", err)
	}
	return users, total, nil
}

// scanScimToken scans one scimTokenColumns row. sql.ErrNoRows becomes
// storage.ErrNotFound.
func scanScimToken(row rowScanner) (storage.ScimToken, error) {
	var tok storage.ScimToken
	err := row.Scan(
		&tok.ID, &tok.Note, timeScan{dst: &tok.CreatedAt}, nullTimeScan{dst: &tok.LastUsedAt},
		&tok.CreatedBy.ID, &tok.CreatedBy.Username, &tok.CreatedBy.DisplayName,
	)
	if err != nil {
		return storage.ScimToken{}, notFound(err)
	}
	return tok, nil
}
