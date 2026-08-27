package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors for the two ways a link can conflict (migration 0012
// enforces both directions).
var (
	// ErrOidcAccountLinked reports that the account already has an identity
	// linked: one row per user, one door per account.
	ErrOidcAccountLinked = errors.New("storage: account already has an sso identity linked")
	// ErrOidcIdentityTaken reports that this (issuer, subject) already
	// belongs to a different account.
	ErrOidcIdentityTaken = errors.New("storage: sso identity is linked to another account")
)

// LinkOidcIdentity records that (issuer, subject) signs in as userID.
// emailAtLink is what the provider claimed at this moment — a forensic
// record for the audit trail, never read to decide anything (migration
// 0012 says why).
func (s *Store) LinkOidcIdentity(ctx context.Context, userID uuid.UUID, issuer, subject string, emailAtLink *string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oidc_identities (user_id, issuer, subject, email_at_link)
		 VALUES ($1, $2, $3, $4)`,
		userID, issuer, subject, emailAtLink,
	)
	if err != nil {
		return fmt.Errorf("link oidc identity: %w", mapOidcConflict(err))
	}
	return nil
}

// NewOidcUser is an account a verified provider assertion is creating
// just-in-time. There is no password hash and no is_admin: the column stays
// NULL because the account has no password credential, and a provider
// assertion is not an administrator's grant. There is no display name
// either — an ID token carries none, and inventing one from the email is
// not this server's to invent.
type NewOidcUser struct {
	// Username is the derived local username (uservalidate.DeriveUsername);
	// a conflict comes back as ErrUsernameTaken for the caller to retry
	// with the next derivation.
	Username string
	// Email is the email claim, or nil when the token carried none. It is
	// stored on the account AND as the identity's forensic email_at_link.
	Email  *string
	Locale string
	// Issuer and Subject are the login key the new account is reachable by.
	Issuer  string
	Subject string
}

// CreateOidcUser provisions an account and links the identity to it in ONE
// transaction, which is the whole reason it is a method rather than two
// calls in the handler: two tabs of one person arriving at once must leave
// one account behind, not one account plus one orphan whose link lost the
// race. The loser gets ErrOidcIdentityTaken (or ErrEmailTaken when the
// token carried an email) and resolves it by looking the identity up again.
func (s *Store) CreateOidcUser(ctx context.Context, nu NewOidcUser) (User, error) {
	var out User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`INSERT INTO users (username, email, locale)
			 VALUES ($1, $2, $3)
			 RETURNING `+userColumns,
			nu.Username, nu.Email, nu.Locale,
		)
		u, err := scanUser(row)
		if err != nil {
			return mapUserConflict(err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO oidc_identities (user_id, issuer, subject, email_at_link)
			 VALUES ($1, $2, $3, $4)`,
			u.ID, nu.Issuer, nu.Subject, nu.Email,
		); err != nil {
			return mapOidcConflict(err)
		}

		// userColumns derives SsoLinked from oidc_identities, and the
		// RETURNING above ran before the row existed. Setting it here
		// rather than re-reading the account is not a shortcut: the insert
		// that makes it true is the statement immediately above, in this
		// transaction.
		u.SsoLinked = true
		out = u
		return nil
	})
	if err != nil {
		return User{}, fmt.Errorf("create oidc user: %w", err)
	}
	return out, nil
}

// UserByOidcIdentity resolves an SSO sign-in: the account (issuer, subject)
// is linked to, recording the use in last_login_at in the same statement.
// (issuer, subject) is the WHOLE lookup — email is deliberately not a
// fallback, which is the account-takeover class migration 0012 exists to
// make impossible. Returns ErrNotFound for an identity linked to nobody.
func (s *Store) UserByOidcIdentity(ctx context.Context, issuer, subject string) (User, error) {
	// memberUserColumns, not userColumns: both tables carry a created_at,
	// so the list must be u-qualified here (the inner EXISTS trivially
	// reads true, which is also correct — this user is linked).
	row := s.pool.QueryRow(ctx,
		`UPDATE oidc_identities ident SET last_login_at = now()
		 FROM users u
		 WHERE ident.issuer = $1 AND ident.subject = $2 AND u.id = ident.user_id
		 RETURNING `+memberUserColumns,
		issuer, subject,
	)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("user by oidc identity: %w", err)
	}
	return u, nil
}

// UnlinkOidcIdentity removes the account's linked identity. Returns
// ErrNotFound when nothing was linked. The no-password refusal is the
// handler's, not this method's: it is an API rule about lockout, and the
// handler already holds the user row it is decided from.
func (s *Store) UnlinkOidcIdentity(ctx context.Context, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM oidc_identities WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("unlink oidc identity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("unlink oidc identity: %w", ErrNotFound)
	}
	return nil
}

// CreateOidcLinkRequest records that the account userID has asked to link
// whatever identity comes back on the flow whose state hashes to stateHash.
// Only POST /users/me/oidc calls this, and only for its own authenticated
// caller — which is what keeps the target account un-forgeable (migration
// 0013). One pending link per state; a state collision would be a crypto/rand
// failure and is refused rather than overwritten.
func (s *Store) CreateOidcLinkRequest(ctx context.Context, stateHash, secretHash []byte, userID uuid.UUID, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oidc_link_requests (state_hash, link_secret_hash, user_id, expires_at)
		 VALUES ($1, $2, $3, now() + make_interval(secs => $4))`,
		stateHash, secretHash, userID, ttl.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("create oidc link request: %w", err)
	}
	return nil
}

// ponytail: expired-but-unconsumed rows are never swept — the per-account
// rate limit bounds how fast they accrue and the table is tiny, so a
// periodic DELETE WHERE expires_at < now() is only worth adding if it ever
// grows. The consuming query already ignores them.

// ConsumeOidcLinkRequest atomically deletes and returns the pending link
// matching BOTH stateHash and secretHash, and only while it is unexpired.
// Requiring both is the two-factor property: the state can be observed by an
// attacker (address bar, history, provider), but the secret only ever lived
// in the cookie of the browser that started the flow, so a forged cookie
// carrying an observed state but a wrong secret matches no row. Both
// conditions plus expiry and single-use are the one statement, so there is
// no read-then-write window a replay or a slow clock could slip through.
// ErrNotFound means there is no live pending link that satisfies all of them
// — which the callback reads as "this is a sign-in, not a link".
func (s *Store) ConsumeOidcLinkRequest(ctx context.Context, stateHash, secretHash []byte) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`DELETE FROM oidc_link_requests
		 WHERE state_hash = $1 AND link_secret_hash = $2 AND expires_at > now()
		 RETURNING user_id`,
		stateHash, secretHash,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("consume oidc link request: %w", err)
	}
	return userID, nil
}

// mapOidcConflict translates the two unique violations of oidc_identities
// into their sentinels. pgx.ErrNoRows never reaches here — inserts do not
// return rows.
func mapOidcConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgerrcode.UniqueViolation {
		return err
	}
	switch pgErr.ConstraintName {
	case "oidc_identities_pkey":
		return ErrOidcAccountLinked
	case "oidc_identities_issuer_subject_key":
		return ErrOidcIdentityTaken
	default:
		return err
	}
}
