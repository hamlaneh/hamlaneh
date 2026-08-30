package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Self-service session management: the read model behind the settings
// Sessions list, plus the two revocation operations that screen offers.
//
// Family semantics, rotation and reuse detection all live in sessions.go and
// are not repeated here — these queries only observe and revoke what that
// model already maintains. Every statement below is scoped WHERE user_id =
// the caller: ownership is a join condition, never a check the caller could
// forget to make afterwards.

// ListSessionFamilies returns one row per live session family of userID:
// the current family first, then most recently active first.
//
// Live means the family's newest generation is neither revoked nor past its
// refresh expiry. Access tokens last minutes, so a device sitting between
// two refreshes is still signed in; the refresh window is what actually
// keeps a family alive.
//
// PostgreSQL picks the newest generation with DISTINCT ON (family_id); SQLite
// has no DISTINCT ON, so the same pick is a ROW_NUMBER window partitioned by
// family_id with the identical ordering, and generation = 1 is the row
// DISTINCT ON would have kept.
//
// The liveness test judges the newest generation and no other, and the two
// steps must stay in that order: folding the filter into the window's
// partition would change the question to "is ANY generation live?" and
// resurrect a revoked family from an older row, because rotation leaves the
// previous generation unrevoked with its refresh window still open. SQLite's
// WHERE-clause push-down optimization does not reach into a subquery that
// uses window functions, so the outer filter cannot migrate inward on its
// own — but that is a property of the optimizer, not the safety net. What
// guards the order is TestListSessionFamiliesJudgesOnlyTheNewestGeneration,
// which fails against a hand-folded query.
//
// The result is deliberately unpaged (contract: bounded by the thirty-day
// refresh TTL, and the artboard draws a flat list).
func (s *Store) ListSessionFamilies(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]storage.SessionFamily, error) {
	rows, err := s.db.QueryContext(ctx,
		`WITH newest AS (
		     SELECT family_id, user_agent, ip, created_at, revoked_at, refresh_expires_at,
		            ROW_NUMBER() OVER (
		                PARTITION BY family_id
		                ORDER BY created_at DESC, id DESC
		            ) AS generation
		     FROM sessions
		     WHERE user_id = ?
		 )
		 SELECT family_id, user_agent, ip, created_at, family_id = ? AS is_current
		 FROM newest
		 WHERE generation = 1
		   AND revoked_at IS NULL
		   AND refresh_expires_at > ?
		 ORDER BY is_current DESC, created_at DESC, family_id`,
		userID, currentFamilyID, s.nowText(),
	)
	if err != nil {
		return nil, fmt.Errorf("list session families: %w", err)
	}
	defer rows.Close()

	families := []storage.SessionFamily{}
	for rows.Next() {
		var fam storage.SessionFamily
		// ip is TEXT here where PostgreSQL declares inet, so the address is
		// decoded in Go rather than by the driver's type map.
		var ip sql.NullString
		if scanErr := rows.Scan(
			&fam.FamilyID, &fam.UserAgent, &ip, timeScan{dst: &fam.LastActiveAt}, &fam.Current,
		); scanErr != nil {
			return nil, fmt.Errorf("list session families: %w", scanErr)
		}
		if ip.Valid {
			addr, parseErr := netip.ParseAddr(ip.String)
			if parseErr != nil {
				return nil, fmt.Errorf("list session families: decode ip %q: %w", ip.String, parseErr)
			}
			fam.IP = &addr
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
// and a family that does not exist at all, both return storage.ErrNotFound:
// one answer for both, so a guessed id never confirms another account's
// session.
func (s *Store) RevokeUserFamily(ctx context.Context, userID, familyID uuid.UUID) error {
	// PostgreSQL writes this as one statement: a data-modifying WITH branch
	// that runs exactly once even though nothing selects from it, alongside an
	// EXISTS that reads the same snapshot — which is what separates "already
	// revoked" (still there, idempotent success) from "not yours" (nothing
	// there, ErrNotFound). SQLite has no data-modifying CTE, so the two halves
	// are two statements, and the write transaction supplies what the shared
	// snapshot supplied there: no other writer can insert or revoke a
	// generation between them, because no other writer can run at all. Both
	// halves carry user_id, so neither can match a family the caller does not
	// own.
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ?
			 WHERE user_id = ? AND family_id = ? AND revoked_at IS NULL`,
			s.nowText(), userID, familyID,
		); err != nil {
			return err
		}

		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM sessions WHERE user_id = ? AND family_id = ?)`,
			userID, familyID,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return storage.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("revoke session family: %w", err)
	}
	return nil
}

// RevokeOtherFamilies revokes every live session family of userID except
// keepFamilyID — "sign out everywhere else". Revoking nothing is success:
// the endpoint answers the same whether or not another device was signed in.
func (s *Store) RevokeOtherFamilies(ctx context.Context, userID, keepFamilyID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ?
		 WHERE user_id = ? AND family_id <> ? AND revoked_at IS NULL`,
		s.nowText(), userID, keepFamilyID,
	)
	if err != nil {
		return fmt.Errorf("revoke other session families: %w", err)
	}
	return nil
}
