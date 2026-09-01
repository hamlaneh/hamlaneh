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

// Single sign-on storage: the oidc_identities and oidc_link_requests tables
// from migrations 0012 and 0013, plus the email lookup the password-reset and
// just-in-time provisioning flows share.

// UserByEmail returns the account with this email, or storage.ErrNotFound.
// The comparison is case-insensitive because the column carries the CITEXT
// collation this driver registers (collation.go), which is where PostgreSQL's
// citext type went.
func (s *Store) UserByEmail(ctx context.Context, email string) (storage.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email)
	u, err := scanUser(row)
	if err != nil {
		return storage.User{}, fmt.Errorf("user by email: %w", err)
	}
	return u, nil
}

// LinkOidcIdentity records that (issuer, subject) signs in as userID.
// emailAtLink is what the provider claimed at this moment — a forensic
// record for the audit trail, never read to decide anything (migration
// 0012 says why).
func (s *Store) LinkOidcIdentity(ctx context.Context, userID uuid.UUID, issuer, subject string, emailAtLink *string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO oidc_identities (user_id, issuer, subject, email_at_link, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		userID, issuer, subject, nullString(emailAtLink), s.nowText(),
	)
	if err != nil {
		return fmt.Errorf("link oidc identity: %w", mapOidcConflict(err))
	}
	return nil
}

// CreateOidcUser provisions an account and links the identity to it in ONE
// transaction, which is the whole reason it is a method rather than two
// calls in the handler: two tabs of one person arriving at once must leave
// one account behind, not one account plus one orphan whose link lost the
// race. The loser gets storage.ErrOidcIdentityTaken (or storage.ErrEmailTaken
// when the token carried an email) and resolves it by looking the identity up
// again.
//
// The PostgreSQL driver gets that from the unique indexes plus its
// transaction; here the transaction alone would be enough, because the second
// arrival cannot start until the first commits and will then see the identity
// and conflict on it. Both hold — the indexes are the invariant, the
// transaction is the atomicity — and neither driver relies on the other.
func (s *Store) CreateOidcUser(ctx context.Context, nu storage.NewOidcUser) (storage.User, error) {
	var out storage.User
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// The id and both timestamps are bound rather than defaulted: this
		// schema tree generates no identifiers and defaults no clock readings
		// (migration 0001).
		now := s.nowText()
		row := tx.QueryRowContext(ctx,
			`INSERT INTO users (id, username, email, locale, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 RETURNING `+userColumns,
			uuid.New(), nu.Username, nullString(nu.Email), nu.Locale, now, now,
		)
		u, err := scanUser(row)
		if err != nil {
			return mapUserConflict(err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO oidc_identities (user_id, issuer, subject, email_at_link, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			u.ID, nu.Issuer, nu.Subject, nullString(nu.Email), now,
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
		return storage.User{}, fmt.Errorf("create oidc user: %w", err)
	}
	return out, nil
}

// UserByOidcIdentity resolves an SSO sign-in: the account (issuer, subject)
// is linked to, recording the use in last_login_at. (issuer, subject) is the
// WHOLE lookup — email is deliberately not a fallback, which is the
// account-takeover class migration 0012 exists to make impossible. Returns
// storage.ErrNotFound for an identity linked to nobody.
//
// storage.UserByOidcIdentity is one statement: UPDATE ... FROM users u ...
// RETURNING the u-qualified user projection. SQLite supports UPDATE ... FROM,
// but its RETURNING clause can only project the table being updated —
// naming u.id there is a "no such column" error, not a join. So the update
// and the read are two statements inside one write transaction, which is
// the same atomic fact: nothing can unlink the identity between them,
// because a competing writer cannot run until this transaction commits.
func (s *Store) UserByOidcIdentity(ctx context.Context, issuer, subject string) (storage.User, error) {
	var out storage.User
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE oidc_identities SET last_login_at = ? WHERE issuer = ? AND subject = ?`,
			s.nowText(), issuer, subject,
		)
		if err != nil {
			return fmt.Errorf("record oidc login: %w", err)
		}
		n, err := rowsAffected(res)
		if err != nil {
			return err
		}
		if n == 0 {
			return storage.ErrNotFound
		}

		// memberUserColumns, not userColumns: both tables carry a created_at,
		// so the list must be u-qualified here (the inner EXISTS trivially
		// reads true, which is also correct — this user is linked).
		out, err = scanUser(tx.QueryRowContext(ctx,
			`SELECT `+memberUserColumns+`
			 FROM users u
			 JOIN oidc_identities i ON i.user_id = u.id
			 WHERE i.issuer = ? AND i.subject = ?`,
			issuer, subject,
		))
		return err
	})
	if err != nil {
		return storage.User{}, fmt.Errorf("user by oidc identity: %w", err)
	}
	return out, nil
}

// UnlinkOidcIdentity removes the account's linked identity. Returns
// storage.ErrNotFound when nothing was linked. The no-password refusal is the
// handler's, not this method's: it is an API rule about lockout, and the
// handler already holds the user row it is decided from.
func (s *Store) UnlinkOidcIdentity(ctx context.Context, userID uuid.UUID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM oidc_identities WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("unlink oidc identity: %w", err)
	}
	n, err := rowsAffected(res)
	if err != nil {
		return fmt.Errorf("unlink oidc identity: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("unlink oidc identity: %w", storage.ErrNotFound)
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
	// PostgreSQL computes the deadline as now() + make_interval; SQLite has no
	// intervals, so it is computed in Go against the same clock every other
	// write in this driver reads.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO oidc_link_requests (state_hash, link_secret_hash, user_id, expires_at)
		 VALUES (?, ?, ?, ?)`,
		stateHash, secretHash, userID, asTime(s.clock().Add(ttl)),
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
// storage.ErrNotFound means there is no live pending link that satisfies all
// of them — which the callback reads as "this is a sign-in, not a link".
//
// DELETE ... RETURNING is the same single statement here as on PostgreSQL;
// only the expiry comparison changes, from now() to a bound timestamp in the
// fixed-width UTC layout whose lexicographic order is chronological.
func (s *Store) ConsumeOidcLinkRequest(ctx context.Context, stateHash, secretHash []byte) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM oidc_link_requests
		 WHERE state_hash = ? AND link_secret_hash = ? AND expires_at > ?
		 RETURNING user_id`,
		stateHash, secretHash, s.nowText(),
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, storage.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("consume oidc link request: %w", err)
	}
	return userID, nil
}

// mapOidcConflict translates the two uniqueness violations of
// oidc_identities into their sentinels. sql.ErrNoRows never reaches here —
// inserts do not return rows.
//
// storage.mapOidcConflict switches on PostgreSQL constraint NAMES
// (oidc_identities_pkey, oidc_identities_issuer_subject_key). SQLite names
// the offending COLUMNS instead: the primary key reports
// "oidc_identities.user_id", and the composite unique index reports both
// "oidc_identities.issuer" and "oidc_identities.subject". The two conflicts
// therefore stay distinguishable, which is what the caller needs — one
// resolves by looking the identity up, the other by telling the user their
// account already has a door.
func mapOidcConflict(err error) error {
	switch {
	case conflictsOn(err, "oidc_identities.user_id"):
		return storage.ErrOidcAccountLinked
	case conflictsOn(err, "oidc_identities.issuer"), conflictsOn(err, "oidc_identities.subject"):
		return storage.ErrOidcIdentityTaken
	default:
		return err
	}
}
