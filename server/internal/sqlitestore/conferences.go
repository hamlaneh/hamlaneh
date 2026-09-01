package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Conference rooms (ADR 005): a link that admits anybody holding it,
// including somebody with no account on this instance. The two properties
// storage/conferences.go decides in the query rather than in a handler are
// decided in the query here too — only the digest is stored, and unknown,
// expired and revoked collapse into one ErrNotFound before anything above
// this layer could tell them apart.
//
// Home mode ships without calls (ADR 012 decision 2), so on a home instance
// nothing ever calls into this file. It is a full port all the same, because
// the driver's contract is the contract's shape and not a deployment's.

// conferenceColumns is the projection every conference query selects, in the
// order scanConference expects. The join is a LEFT join because created_by
// is nullable.
const conferenceColumns = `c.id, c.title, c.created_at, c.expires_at, u.id, u.username, u.display_name`

// liveConference is the predicate for "this link still admits somebody": not
// revoked, and not past an expiry it may not even have. The preview and the
// join judge liveness by this one expression, so they cannot disagree about
// it — which is what makes their two refusals the same refusal.
//
// PostgreSQL compares against now(); SQLite has no such function, so the
// expression ends in a bound parameter every caller fills with s.nowText().
const liveConference = `c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at > ?)`

// unrevokedConference is the weaker predicate the owner's own views use: a
// link whose expiry has passed is dead to a visitor but still a row its
// owner may want to see and take off the list.
const unrevokedConference = `c.revoked_at IS NULL`

// CreateConference stores one conference and returns it. tokenHash is the
// SHA-256 digest of the generated link; the raw value never reaches the
// database. expiresAt nil is a link that does not expire.
//
// PostgreSQL writes this as one data-modifying CTE — INSERT ... RETURNING
// feeding a left join against users. SQLite has no data-modifying CTEs, so
// the insert and the read-back are two statements in one write transaction:
// nothing can come between them, because this transaction holds the
// database's write lock from BEGIN.
func (s *Store) CreateConference(ctx context.Context, createdBy uuid.UUID, tokenHash []byte,
	title string, expiresAt *time.Time,
) (storage.Conference, error) {
	var (
		id   = uuid.New()
		now  = s.clock()
		conf storage.Conference
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conferences (id, created_by, title, link_token_hash, expires_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, createdBy, title, tokenHash, asNullTime(expiresAt), asTime(now), asTime(now),
		); err != nil {
			return err
		}
		var scanErr error
		conf, scanErr = scanConference(tx.QueryRowContext(ctx,
			`SELECT `+conferenceColumns+`
			 FROM conferences c LEFT JOIN users u ON u.id = c.created_by
			 WHERE c.id = ?`,
			id,
		))
		return scanErr
	})
	if err != nil {
		return storage.Conference{}, fmt.Errorf("create conference: %w", err)
	}
	return conf, nil
}

// ListConferences returns the conferences one caller may see, newest first:
// their own, or every one on the instance when all is set — an administrator
// must be able to find what they may revoke.
//
// Revoked conferences leave the list because the question the list answers
// is which links are live; the audit log is where the history lives.
func (s *Store) ListConferences(ctx context.Context, ownerID uuid.UUID, all bool) ([]storage.Conference, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+conferenceColumns+`
		 FROM conferences c LEFT JOIN users u ON u.id = c.created_by
		 WHERE `+unrevokedConference+` AND (? = 1 OR c.created_by = ?)
		 ORDER BY c.created_at DESC, c.id`,
		boolValue(all), ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list conferences: %w", err)
	}
	// See ListChannelsForUser on why the close error is discarded here.
	defer func() { _ = rows.Close() }()

	conferences := []storage.Conference{}
	for rows.Next() {
		conf, scanErr := scanConference(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list conferences: %w", scanErr)
		}
		conferences = append(conferences, conf)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list conferences: %w", err)
	}
	return conferences, nil
}

// ConferenceByID resolves one conference by the id a signed-in caller named.
//
// A revoked conference answers ErrNotFound: a revoked link is gone for every
// purpose this API has — not listed, not joinable, not revocable a second
// time — so "already revoked" and "never existed" are one answer here for
// the same reason the three link failures are one answer below. An expired
// one is still returned: its owner may want it off their list.
//
// It says nothing about who may act on the row. That decision belongs to
// internal/authz, which needs the owner this returns in order to make it.
func (s *Store) ConferenceByID(ctx context.Context, id uuid.UUID) (storage.Conference, error) {
	conf, err := scanConference(s.db.QueryRowContext(ctx,
		`SELECT `+conferenceColumns+`
		 FROM conferences c LEFT JOIN users u ON u.id = c.created_by
		 WHERE c.id = ? AND `+unrevokedConference,
		id,
	))
	if err != nil {
		return storage.Conference{}, fmt.Errorf("conference by id: %w", err)
	}
	return conf, nil
}

// RevokeConference kills one link, reporting ErrNotFound when no live
// conference had that id.
//
// Unlike invitation revocation, which is idempotent, the contract reserves a
// 404 here — the same shape a provisioning token's revocation has, and for
// the same reason: somebody cutting a door off needs to know when they named
// the wrong one.
//
// It kills the link only. Ending the meeting behind it is the caller's
// second act (internal/calls), because a revocation that let the current
// meeting run on would not be a revocation.
func (s *Store) RevokeConference(ctx context.Context, id uuid.UUID) error {
	now := s.nowText()
	res, err := s.db.ExecContext(ctx,
		`UPDATE conferences SET revoked_at = ?, updated_at = ?
		 WHERE id = ? AND revoked_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		return fmt.Errorf("revoke conference: %w", err)
	}
	n, err := rowsAffected(res)
	if err != nil {
		return fmt.Errorf("revoke conference: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("revoke conference: %w", storage.ErrNotFound)
	}
	return nil
}

// LiveConferenceByTokenHash resolves a presented link to the conference it
// admits to.
//
// Unknown, expired and revoked are ONE answer here — ErrNotFound — because
// they are one answer in the contract: a visitor learns whether their link
// works, never why it does not. All three take the same path through this
// one query, so they cost the same work as well as reading the same: a
// revoked row is not in conferences_live_idx at all, and an expired one
// fails the predicate the same way a wrong digest fails the index probe.
//
// The comparison is over the SHA-256 digest and never over the presented
// link. That is the invitation path's defense and it is the whole of it: the
// raw value takes part in no comparison anywhere, so there is no timing
// signal on a secret to be had — a byte-at-a-time oracle on a digest
// comparison would need preimage control over SHA-256 to exploit.
func (s *Store) LiveConferenceByTokenHash(ctx context.Context, tokenHash []byte) (storage.Conference, error) {
	conf, err := scanConference(s.db.QueryRowContext(ctx,
		`SELECT `+conferenceColumns+`
		 FROM conferences c LEFT JOIN users u ON u.id = c.created_by
		 WHERE c.link_token_hash = ? AND `+liveConference,
		tokenHash, s.nowText(),
	))
	if err != nil {
		return storage.Conference{}, fmt.Errorf("conference by link: %w", err)
	}
	return conf, nil
}

// scanConference scans one conferenceColumns row. sql.ErrNoRows becomes
// storage.ErrNotFound; a null creator becomes a nil CreatedBy rather than a
// zero one, so "the account is gone" cannot be mistaken for "created by
// nobody".
func scanConference(row rowScanner) (storage.Conference, error) {
	var (
		conf        storage.Conference
		creatorID   uuid.NullUUID
		username    sql.NullString
		displayName sql.NullString
	)
	err := row.Scan(
		&conf.ID, &conf.Title, timeScan{dst: &conf.CreatedAt}, nullTimeScan{dst: &conf.ExpiresAt},
		&creatorID, &username, &displayName,
	)
	if err != nil {
		return storage.Conference{}, notFound(err)
	}
	if creatorID.Valid {
		conf.CreatedBy = &storage.ConferenceCreator{
			ID:          creatorID.UUID,
			Username:    username.String,
			DisplayName: displayName.String,
		}
	}
	return conf, nil
}
