package storage

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// Self-service session management: the read model behind the settings
// Sessions list, plus the two revocation operations that screen offers.
//
// Family semantics, rotation and reuse detection all live in sessions.go and
// are not repeated here — these queries only observe and revoke what that
// model already maintains. Every statement below is scoped WHERE user_id =
// the caller: ownership is a join condition, never a check the caller could
// forget to make afterwards.

// SessionFamily is one signed-in device: a refresh-token family described by
// its newest generation (migration 0002 — a device in the UI is a family).
type SessionFamily struct {
	FamilyID  uuid.UUID
	UserAgent string
	// IP is the newest generation's recorded address, nil when none was
	// recorded (the column is nullable).
	IP *netip.Addr
	// LastActiveAt is the newest generation's created_at. Refreshing is the
	// only heartbeat the server observes, so the precision of this value is
	// the access-token lifetime — the UI labels these rows approximate.
	LastActiveAt time.Time
	// Current marks the family that owns the session asking for the list.
	// The caller derives it from the request's own session; nothing the
	// client sends can select it.
	Current bool
}

// ListSessionFamilies returns one row per live session family of userID:
// the current family first, then most recently active first.
//
// Live means the family's newest generation is neither revoked nor past its
// refresh expiry. Access tokens last minutes, so a device sitting between
// two refreshes is still signed in; the refresh window is what actually
// keeps a family alive.
//
// The liveness test judges the newest generation and no other, and the two
// steps must stay in that order: folding the filter into the DISTINCT ON
// would change the question to "is ANY generation live?" and resurrect a
// revoked family from an older row, because rotation leaves the previous
// generation unrevoked with its refresh window still open. MATERIALIZED
// says so in the query, but it is not what enforces it — PostgreSQL will
// not push a qual on a non-DISTINCT-ON column through the Unique node
// whether the CTE is fenced or not (verified on 17: the plan keeps the
// filter above Unique either way). What actually guards the order is
// TestListSessionFamiliesJudgesOnlyTheNewestGeneration, which fails against
// a hand-folded query. Do not read the keyword as the safety net.
//
// The result is deliberately unpaged (contract: bounded by the thirty-day
// refresh TTL, and the artboard draws a flat list).
func (s *Store) ListSessionFamilies(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]SessionFamily, error) {
	rows, err := s.pool.Query(ctx,
		`WITH newest AS MATERIALIZED (
		     SELECT DISTINCT ON (family_id)
		            family_id, user_agent, ip, created_at, revoked_at, refresh_expires_at
		     FROM sessions
		     WHERE user_id = $1
		     ORDER BY family_id, created_at DESC, id DESC
		 )
		 SELECT family_id, user_agent, ip, created_at, family_id = $2
		 FROM newest
		 WHERE revoked_at IS NULL
		   AND refresh_expires_at > now()
		 ORDER BY (family_id = $2) DESC, created_at DESC, family_id`,
		userID, currentFamilyID,
	)
	if err != nil {
		return nil, fmt.Errorf("list session families: %w", err)
	}
	defer rows.Close()

	families := []SessionFamily{}
	for rows.Next() {
		var fam SessionFamily
		if scanErr := rows.Scan(
			&fam.FamilyID, &fam.UserAgent, &fam.IP, &fam.LastActiveAt, &fam.Current,
		); scanErr != nil {
			return nil, fmt.Errorf("list session families: %w", scanErr)
		}
		families = append(families, fam)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list session families: %w", err)
	}
	return families, nil
}

// RevokeUserFamily revokes every generation of one session family, scoped to
// its owner. Revoking the whole family rather than its newest generation is
// what the reuse-detection model requires: a half-revoked family would leave
// a live refresh token behind, and the next rotation would mint a fresh
// session from it.
//
// It is idempotent — an already-revoked family of the same user succeeds and
// keeps its original revocation time. A family belonging to somebody else,
// and a family that does not exist at all, both return ErrNotFound: one
// answer for both, so a guessed id never confirms another account's session.
func (s *Store) RevokeUserFamily(ctx context.Context, userID, familyID uuid.UUID) error {
	// One statement, two jobs. PostgreSQL runs a data-modifying WITH branch
	// exactly once and to completion even when nothing selects from it, and
	// the EXISTS reads the same snapshot — which is what separates "already
	// revoked" (still there, idempotent success) from "not yours" (nothing
	// there, ErrNotFound). Both halves carry user_id, so neither can match a
	// family the caller does not own.
	var exists bool
	err := s.pool.QueryRow(ctx,
		`WITH revoked AS (
		     UPDATE sessions SET revoked_at = now()
		     WHERE user_id = $1 AND family_id = $2 AND revoked_at IS NULL
		 )
		 SELECT EXISTS (SELECT 1 FROM sessions WHERE user_id = $1 AND family_id = $2)`,
		userID, familyID,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("revoke session family: %w", err)
	}
	if !exists {
		return fmt.Errorf("revoke session family: %w", ErrNotFound)
	}
	return nil
}

// RevokeOtherFamilies revokes every live session family of userID except
// keepFamilyID — "sign out everywhere else". Revoking nothing is success:
// the endpoint answers the same whether or not another device was signed in.
func (s *Store) RevokeOtherFamilies(ctx context.Context, userID, keepFamilyID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now()
		 WHERE user_id = $1 AND family_id <> $2 AND revoked_at IS NULL`,
		userID, keepFamilyID,
	)
	if err != nil {
		return fmt.Errorf("revoke other session families: %w", err)
	}
	return nil
}
