package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Conference rooms (ADR 005): a link that admits anybody holding it,
// including somebody with no account on this instance. It is the widest door
// in the product, and two of the three things that keep it narrow are
// decided in this file rather than in a handler.
//
//   - **Only the digest is stored.** Nothing here takes or returns a raw
//     link; a stolen dump yields nothing that can be presented. Same posture
//     as invitations and provisioning tokens.
//   - **Unknown, expired and revoked are ONE answer** — ErrNotFound — and
//     they are collapsed at the query rather than at the handler. The
//     distinction is then not available to be leaked further up, which is a
//     stronger guarantee than two handlers that agree today.
//
// The third — that a link buys one media room and nothing else — is not a
// storage property at all: it is the grant set on the ticket, in
// internal/calls.

// Conference is one conference room as its owner or an administrator sees
// it. The link is deliberately absent: only its digest exists here.
type Conference struct {
	ID uuid.UUID
	// CreatedBy is nil when the account that made it is gone. The row
	// outlives that account on purpose (migration 0016) — somebody may still
	// be meeting in it — and an administrator can still revoke what has no
	// owner left.
	CreatedBy *ConferenceCreator
	Title     string
	CreatedAt time.Time
	// ExpiresAt nil means it does not expire, which is the default and is
	// deliberate (ADR 005).
	ExpiresAt *time.Time
}

// ConferenceCreator is who made a conference: exactly the three fields the
// contract's UserSummary carries, as InviteCreator is for an invitation.
type ConferenceCreator struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
}

// conferenceColumns is the projection every conference query selects, in the
// order scanConference expects. The join is a LEFT join because created_by
// is nullable.
const conferenceColumns = `c.id, c.title, c.created_at, c.expires_at, u.id, u.username, u.display_name`

// liveConference is the predicate for "this link still admits somebody": not
// revoked, and not past an expiry it may not even have. The preview and the
// join judge liveness by this one expression, so they cannot disagree about
// it — which is what makes their two refusals the same refusal.
const liveConference = `c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at > now())`

// unrevokedConference is the weaker predicate the owner's own views use: a
// link whose expiry has passed is dead to a visitor but still a row its
// owner may want to see and take off the list.
const unrevokedConference = `c.revoked_at IS NULL`

// CreateConference stores one conference and returns it. tokenHash is the
// SHA-256 digest of the generated link; the raw value never reaches the
// database. expiresAt nil is a link that does not expire.
func (s *Store) CreateConference(ctx context.Context, createdBy uuid.UUID, tokenHash []byte,
	title string, expiresAt *time.Time,
) (Conference, error) {
	row := s.pool.QueryRow(ctx,
		`WITH new_conference AS (
		     INSERT INTO conferences (created_by, title, link_token_hash, expires_at)
		     VALUES ($1, $2, $3, $4)
		     RETURNING id, title, created_at, expires_at, created_by
		 )
		 SELECT `+conferenceColumns+`
		 FROM new_conference c LEFT JOIN users u ON u.id = c.created_by`,
		createdBy, title, tokenHash, expiresAt,
	)
	conf, err := scanConference(row)
	if err != nil {
		return Conference{}, fmt.Errorf("create conference: %w", err)
	}
	return conf, nil
}

// ListConferences returns the conferences one caller may see, newest first:
// their own, or every one on the instance when all is set — an administrator
// must be able to find what they may revoke.
//
// Revoked conferences leave the list because the question the list answers
// is which links are live; the audit log is where the history lives.
func (s *Store) ListConferences(ctx context.Context, ownerID uuid.UUID, all bool) ([]Conference, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+conferenceColumns+`
		 FROM conferences c LEFT JOIN users u ON u.id = c.created_by
		 WHERE `+unrevokedConference+` AND ($1 OR c.created_by = $2)
		 ORDER BY c.created_at DESC, c.id`,
		all, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list conferences: %w", err)
	}
	defer rows.Close()

	conferences := []Conference{}
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
func (s *Store) ConferenceByID(ctx context.Context, id uuid.UUID) (Conference, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+conferenceColumns+`
		 FROM conferences c LEFT JOIN users u ON u.id = c.created_by
		 WHERE c.id = $1 AND `+unrevokedConference,
		id,
	)
	conf, err := scanConference(row)
	if err != nil {
		return Conference{}, fmt.Errorf("conference by id: %w", err)
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
	tag, err := s.pool.Exec(ctx,
		`UPDATE conferences SET revoked_at = now(), updated_at = now()
		 WHERE id = $1 AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("revoke conference: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoke conference: %w", ErrNotFound)
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
func (s *Store) LiveConferenceByTokenHash(ctx context.Context, tokenHash []byte) (Conference, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+conferenceColumns+`
		 FROM conferences c LEFT JOIN users u ON u.id = c.created_by
		 WHERE c.link_token_hash = $1 AND `+liveConference,
		tokenHash,
	)
	conf, err := scanConference(row)
	if err != nil {
		return Conference{}, fmt.Errorf("conference by link: %w", err)
	}
	return conf, nil
}

// scanConference scans one conferenceColumns row. pgx.ErrNoRows becomes
// ErrNotFound; a null creator becomes a nil CreatedBy rather than a zero one,
// so "the account is gone" cannot be mistaken for "created by nobody".
func scanConference(row pgx.Row) (Conference, error) {
	var (
		conf        Conference
		creatorID   *uuid.UUID
		username    *string
		displayName *string
	)
	err := row.Scan(&conf.ID, &conf.Title, &conf.CreatedAt, &conf.ExpiresAt,
		&creatorID, &username, &displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conference{}, ErrNotFound
	}
	if err != nil {
		return Conference{}, err
	}
	if creatorID != nil {
		conf.CreatedBy = &ConferenceCreator{ID: *creatorID}
		if username != nil {
			conf.CreatedBy.Username = *username
		}
		if displayName != nil {
			conf.CreatedBy.DisplayName = *displayName
		}
	}
	return conf, nil
}
