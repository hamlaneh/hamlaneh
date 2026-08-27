package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SCIM provisioning storage (docs/api/scim.md): the credentials an identity
// provider's sync engine authenticates with, and the account reads and
// writes it makes.
//
// Nothing in this file can change is_admin or is_active. Roles stay a
// deliberate act in the dashboard, so a compromised sync token cannot mint
// an administrator (§2), and deactivation goes through UpdateUserAdmin —
// which owns the advisory lock, the last-administrator rule and the session
// revocation. One kill path, not two (§5).

// ScimToken is one row of scim_tokens as the dashboard lists it. The token
// is deliberately absent: only its digest is stored, exactly as invitations
// and reset tokens do it, so nothing here can redisplay a credential.
type ScimToken struct {
	ID        uuid.UUID
	Note      string
	CreatedBy ScimTokenCreator
	CreatedAt time.Time
	// LastUsedAt is nil until a provider first authenticates with the token,
	// which is how a configured credential is told apart from one that was
	// minted and forgotten.
	LastUsedAt *time.Time
}

// ScimTokenCreator is the administrator who minted a token: the same three
// fields the contract's UserSummary carries, exactly as InviteCreator is for
// an invitation.
type ScimTokenCreator struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
}

// scimTokenColumns is the projection every token query selects, in the order
// scanScimToken expects.
const scimTokenColumns = `t.id, t.note, t.created_at, t.last_used_at, u.id, u.username, u.display_name`

// CreateScimToken stores one provisioning credential and returns it.
// tokenHash is the SHA-256 digest of the generated token; the raw value
// never reaches the database.
func (s *Store) CreateScimToken(ctx context.Context, createdBy uuid.UUID, tokenHash []byte, note string) (ScimToken, error) {
	row := s.pool.QueryRow(ctx,
		`WITH new_token AS (
		     INSERT INTO scim_tokens (token_hash, created_by, note)
		     VALUES ($1, $2, $3)
		     RETURNING id, note, created_at, last_used_at, created_by
		 )
		 SELECT `+scimTokenColumns+`
		 FROM new_token t JOIN users u ON u.id = t.created_by`,
		tokenHash, createdBy, note,
	)
	tok, err := scanScimToken(row)
	if err != nil {
		return ScimToken{}, fmt.Errorf("create scim token: %w", err)
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
func (s *Store) ListScimTokens(ctx context.Context) ([]ScimToken, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+scimTokenColumns+`
		 FROM scim_tokens t JOIN users u ON u.id = t.created_by
		 WHERE t.revoked_at IS NULL
		 ORDER BY t.created_at DESC, t.id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list scim tokens: %w", err)
	}
	defer rows.Close()

	tokens := []ScimToken{}
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
	tag, err := s.pool.Exec(ctx,
		`UPDATE scim_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("revoke scim token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoke scim token: %w", ErrNotFound)
	}
	return nil
}

// scimTokenTouchInterval is how stale last_used_at may get before a use
// writes it again.
//
// The column exists to tell a configured credential from one that was minted
// and forgotten, and that question does not need second-level precision.
// Writing on every request would turn a provider's full sync — which is
// hundreds of reads — into hundreds of same-row UPDATEs, each one a dead
// tuple for autovacuum, for a value nobody reads at that resolution.
const scimTokenTouchInterval = time.Minute

// ScimTokenByHash resolves a presented bearer token to the id of the live
// credential behind it, recording the use unless it was already recorded
// within scimTokenTouchInterval.
//
// The lookup and the touch are one statement, and the touch is the part that
// may do nothing: the CTE resolves the credential first and the UPDATE runs
// against that result, so the id comes back whether or not the timestamp
// moved. Making the UPDATE itself conditional would have returned no row on
// the requests it skipped, which is a 401 for a perfectly good token.
//
// Unknown, revoked and never-issued digests are one answer — ErrNotFound —
// because they are one answer at the door: 401, with nothing said about
// which of the three it was.
func (s *Store) ScimTokenByHash(ctx context.Context, tokenHash []byte) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx,
		`WITH live AS (
		     SELECT id, last_used_at FROM scim_tokens
		     WHERE token_hash = $1 AND revoked_at IS NULL
		 ), touched AS (
		     UPDATE scim_tokens SET last_used_at = now()
		     WHERE id IN (
		         SELECT id FROM live
		         WHERE last_used_at IS NULL
		            OR last_used_at < now() - make_interval(secs => $2)
		     )
		 )
		 SELECT id FROM live`,
		tokenHash, scimTokenTouchInterval.Seconds(),
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("scim token by hash: %w", ErrNotFound)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("scim token by hash: %w", err)
	}
	return id, nil
}

// NewScimUser is an account a directory is creating. There is no password
// hash and no is_admin: the column stays NULL because a provisioned account
// has no password credential, and the role is not a directory's to set.
type NewScimUser struct {
	// Username is the derived local username (uservalidate.DeriveUsername),
	// not the provider's userName — the account rules stay as they are.
	Username     string
	ScimUserName string
	ExternalID   *string
	Email        *string
	DisplayName  string
	Locale       string
	IsActive     bool
}

// CreateScimUser inserts a directory-provisioned account and returns the
// stored row. A local username collision maps to ErrUsernameTaken, which the
// caller resolves by deriving a suffixed one; a userName, externalId or
// email already held by somebody else maps to ErrScimIdentifierTaken or
// ErrEmailTaken, which are the 409 the provider must see.
func (s *Store) CreateScimUser(ctx context.Context, nu NewScimUser) (User, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, email, display_name, locale, is_active, scim_user_name, scim_external_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+userColumns,
		nu.Username, nu.Email, nu.DisplayName, nu.Locale, nu.IsActive, nu.ScimUserName, nu.ExternalID,
	)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("create scim user: %w", mapUserConflict(err))
	}
	return u, nil
}

// ScimUserAttributes is the whole writable attribute set of §4 — everything
// a directory may change and nothing else. A nil pointer is SQL NULL, not
// "leave alone": PUT replaces and PATCH is applied onto the current values
// before it gets here, so one write path serves both and neither can reach a
// column this struct does not name.
type ScimUserAttributes struct {
	ScimUserName string
	ExternalID   *string
	Email        *string
	DisplayName  string
}

// ReplaceScimUser writes the mapped attributes of one account and returns
// the stored row, or ErrNotFound when the id names nobody.
//
// is_admin and is_active are absent from the UPDATE by construction, which
// is what makes "SCIM cannot mint an administrator" a property of this
// statement rather than of a check somebody has to remember.
func (s *Store) ReplaceScimUser(ctx context.Context, id uuid.UUID, attrs ScimUserAttributes) (User, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE users
		 SET scim_user_name    = $1,
		     scim_external_id  = $2,
		     email             = $3,
		     display_name      = $4,
		     updated_at        = now()
		 WHERE id = $5
		 RETURNING `+userColumns,
		attrs.ScimUserName, attrs.ExternalID, attrs.Email, attrs.DisplayName, id,
	)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("replace scim user: %w", mapUserConflict(err))
	}
	return u, nil
}

// ScimUserFilter is the whole of the filter language §2 supports: equality
// on userName or on externalId. A nil field is a filter the request did not
// carry.
type ScimUserFilter struct {
	// UserName matches scim_user_name OR email. The second matcher is what
	// lets a provider adopt an account somebody already made locally (§4):
	// the create answers 409, the provider's lookup finds it by email, and
	// the PUT that follows sets externalId and marks it directory-managed.
	UserName *string
	// ExternalID matches scim_external_id and nothing else.
	ExternalID *string
}

// ListScimUsers returns one page of accounts plus the total the filter
// matches, in stable (created_at, id) order. offset is zero-based; a limit
// of zero returns the total and no rows, which is what a SCIM count=0 asks
// for.
func (s *Store) ListScimUsers(ctx context.Context, f ScimUserFilter, offset, limit int) ([]User, int, error) {
	// One WHERE clause, written once and used by both statements, so the
	// page and the total can never be counting different things.
	const where = `WHERE ($1::citext IS NULL OR scim_user_name = $1::citext OR email = $1::citext)
	                 AND ($2::text IS NULL OR scim_external_id = $2::text)`

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users `+where, f.UserName, f.ExternalID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count scim users: %w", err)
	}
	if limit <= 0 {
		return []User{}, total, nil
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+userColumns+` FROM users `+where+`
		 ORDER BY created_at, id
		 OFFSET $3 LIMIT $4`,
		f.UserName, f.ExternalID, offset, limit,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list scim users: %w", err)
	}
	defer rows.Close()

	users := []User{}
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

// scanScimToken scans one scimTokenColumns row. pgx.ErrNoRows becomes
// ErrNotFound.
func scanScimToken(row pgx.Row) (ScimToken, error) {
	var tok ScimToken
	err := row.Scan(
		&tok.ID, &tok.Note, &tok.CreatedAt, &tok.LastUsedAt,
		&tok.CreatedBy.ID, &tok.CreatedBy.Username, &tok.CreatedBy.DisplayName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScimToken{}, ErrNotFound
	}
	if err != nil {
		return ScimToken{}, err
	}
	return tok, nil
}
