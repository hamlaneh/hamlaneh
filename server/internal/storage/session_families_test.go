package storage_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// deviceTokens builds SessionTokens carrying a recognisable device
// fingerprint, so a listed row can be traced to the generation it came from.
func deviceTokens(access, refresh, userAgent, ip string) storage.SessionTokens {
	tokens := tokensFor(access, refresh)
	tokens.UserAgent = userAgent
	addr := netip.MustParseAddr(ip)
	tokens.IP = &addr
	return tokens
}

// assertionPool opens a second pool for raw table assertions; Store
// deliberately does not expose its own.
func assertionPool(ctx context.Context, t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open assertion pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// familyRowCounts reads how many generations a family has and how many of
// them are revoked, straight from the table — revocation is judged on rows,
// not on the read model's view of them.
func familyRowCounts(ctx context.Context, t *testing.T, pool *pgxpool.Pool, familyID uuid.UUID) (total, revoked int) {
	t.Helper()

	err := pool.QueryRow(ctx,
		`SELECT count(*), count(revoked_at) FROM sessions WHERE family_id = $1`,
		familyID,
	).Scan(&total, &revoked)
	if err != nil {
		t.Fatalf("count generations of family %s: %v", familyID, err)
	}
	return total, revoked
}

// familyIDs projects the listed families onto their ids, for order assertions.
func familyIDs(families []storage.SessionFamily) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(families))
	for _, fam := range families {
		ids = append(ids, fam.FamilyID)
	}
	return ids
}

// mustListFamilies lists the caller's families or fails the test.
func mustListFamilies(ctx context.Context, t *testing.T, store *storage.Store, userID, currentFamilyID uuid.UUID) []storage.SessionFamily {
	t.Helper()

	families, err := store.ListSessionFamilies(ctx, userID, currentFamilyID)
	if err != nil {
		t.Fatalf("ListSessionFamilies: %v", err)
	}
	return families
}

// TestListSessionFamiliesIntegration pins the settings Sessions read model:
// one row per live family, described by its newest generation, the caller's
// own family first, and never a row belonging to another account.
func TestListSessionFamiliesIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	pool := assertionPool(ctx, t, dsn)

	user := mustCreateUser(ctx, t, store, newUser("lister"))
	stranger := mustCreateUser(ctx, t, store, newUser("listerstranger"))

	// Three devices in age order. The caller is the OLDEST one, so
	// current-first ordering has to beat recency for this to pass.
	deviceA := mustCreateSession(ctx, t, store, user.ID, deviceTokens("a-acc", "a-ref", "device-a", "203.0.113.10"))
	deviceB := mustCreateSession(ctx, t, store, user.ID, deviceTokens("b-acc", "b-ref", "device-b", "203.0.113.11"))
	deviceC := mustCreateSession(ctx, t, store, user.ID, deviceTokens("c-acc", "c-ref", "device-c", "203.0.113.12"))

	strangerSess := mustCreateSession(ctx, t, store, stranger.ID,
		deviceTokens("s-acc", "s-ref", "device-stranger", "203.0.113.99"))

	t.Run("current family first, then most recently active", func(t *testing.T) {
		families := mustListFamilies(ctx, t, store, user.ID, deviceA.FamilyID)

		want := []uuid.UUID{deviceA.FamilyID, deviceC.FamilyID, deviceB.FamilyID}
		if got := familyIDs(families); !slices.Equal(got, want) {
			t.Fatalf("family order:\n got %v\nwant %v", got, want)
		}
		if !families[0].Current {
			t.Error("the caller's own family is not marked current")
		}
		for _, fam := range families[1:] {
			if fam.Current {
				t.Errorf("family %s is marked current but did not make the request", fam.FamilyID)
			}
		}
	})

	t.Run("each row carries its own device details", func(t *testing.T) {
		families := mustListFamilies(ctx, t, store, user.ID, deviceA.FamilyID)

		want := map[uuid.UUID][2]string{
			deviceA.FamilyID: {"device-a", "203.0.113.10"},
			deviceB.FamilyID: {"device-b", "203.0.113.11"},
			deviceC.FamilyID: {"device-c", "203.0.113.12"},
		}
		for _, fam := range families {
			expect, ok := want[fam.FamilyID]
			if !ok {
				t.Fatalf("unexpected family %s in the list", fam.FamilyID)
			}
			if fam.UserAgent != expect[0] {
				t.Errorf("family %s user agent = %q, want %q", fam.FamilyID, fam.UserAgent, expect[0])
			}
			if fam.IP == nil || fam.IP.String() != expect[1] {
				t.Errorf("family %s ip = %v, want %s", fam.FamilyID, fam.IP, expect[1])
			}
			if fam.LastActiveAt.IsZero() {
				t.Errorf("family %s has no last_active_at", fam.FamilyID)
			}
		}
	})

	t.Run("another account's families never appear", func(t *testing.T) {
		for _, fam := range mustListFamilies(ctx, t, store, user.ID, deviceA.FamilyID) {
			if fam.FamilyID == strangerSess.FamilyID {
				t.Fatal("the stranger's family leaked into the caller's list")
			}
		}

		// And the scoping cuts both ways: the stranger sees only their own.
		strangerFamilies := mustListFamilies(ctx, t, store, stranger.ID, strangerSess.FamilyID)
		want := []uuid.UUID{strangerSess.FamilyID}
		if got := familyIDs(strangerFamilies); !slices.Equal(got, want) {
			t.Errorf("stranger's list:\n got %v\nwant %v", got, want)
		}
	})

	t.Run("rotation moves a family up and refreshes its device details", func(t *testing.T) {
		_, outcome, err := store.RotateSession(ctx, hashOf("b-ref"),
			deviceTokens("b2-acc", "b2-ref", "device-b-renamed", "203.0.113.21"))
		if err != nil || outcome != storage.RotateOutcomeRotated {
			t.Fatalf("rotate device B: outcome %v, err %v", outcome, err)
		}

		families := mustListFamilies(ctx, t, store, user.ID, deviceA.FamilyID)
		want := []uuid.UUID{deviceA.FamilyID, deviceB.FamilyID, deviceC.FamilyID}
		if got := familyIDs(families); !slices.Equal(got, want) {
			t.Fatalf("family order after rotation:\n got %v\nwant %v", got, want)
		}

		rotated := families[1]
		if rotated.UserAgent != "device-b-renamed" {
			t.Errorf("user agent = %q, want the newest generation's device-b-renamed", rotated.UserAgent)
		}
		if rotated.IP == nil || rotated.IP.String() != "203.0.113.21" {
			t.Errorf("ip = %v, want the newest generation's 203.0.113.21", rotated.IP)
		}

		// Two generations exist; the list still shows the family once.
		if total, _ := familyRowCounts(ctx, t, pool, deviceB.FamilyID); total != 2 {
			t.Errorf("device B has %d generations, want 2", total)
		}
	})

	t.Run("a revoked family disappears", func(t *testing.T) {
		if err := store.RevokeFamily(ctx, deviceC.FamilyID); err != nil {
			t.Fatalf("RevokeFamily: %v", err)
		}

		families := mustListFamilies(ctx, t, store, user.ID, deviceA.FamilyID)
		want := []uuid.UUID{deviceA.FamilyID, deviceB.FamilyID}
		if got := familyIDs(families); !slices.Equal(got, want) {
			t.Errorf("list after revoking device C:\n got %v\nwant %v", got, want)
		}
	})

	t.Run("a family past its refresh expiry disappears", func(t *testing.T) {
		tokens := deviceTokens("exp-acc", "exp-ref", "device-expired", "203.0.113.30")
		tokens.RefreshTTL = -time.Second // dead at birth, judged by the database clock
		expired := mustCreateSession(ctx, t, store, user.ID, tokens)

		for _, fam := range mustListFamilies(ctx, t, store, user.ID, deviceA.FamilyID) {
			if fam.FamilyID == expired.FamilyID {
				t.Fatal("a family whose refresh window closed is still listed as signed in")
			}
		}
	})

	t.Run("no sessions yields an empty, non-nil list", func(t *testing.T) {
		quiet := mustCreateUser(ctx, t, store, newUser("listerquiet"))

		families, err := store.ListSessionFamilies(ctx, quiet.ID, uuid.Nil)
		if err != nil {
			t.Fatalf("ListSessionFamilies: %v", err)
		}
		if families == nil {
			t.Error("got a nil slice; the contract's sessions array must never be null")
		}
		if len(families) != 0 {
			t.Errorf("got %d families for a user who never signed in", len(families))
		}
	})
}

// TestListSessionFamiliesJudgesOnlyTheNewestGeneration pins the ordering
// ListSessionFamilies depends on: pick each family's newest generation
// FIRST, judge liveness SECOND.
//
// Fold the liveness filter into the DISTINCT ON and the question silently
// becomes "is ANY generation of this family live?", which resurrects a
// revoked family from an older row — rotation leaves the previous
// generation unrevoked with its refresh window still open, so there is
// always one sitting there to be picked. The fixture below is exactly that
// shape: two generations, only the newest revoked.
//
// Note what this test does NOT depend on. Deleting MATERIALIZED from the
// query does not fail it, because PostgreSQL never pushes a qual on a
// non-DISTINCT-ON column through the Unique node; the plan keeps the filter
// above it either way. The keyword documents the intent, this test enforces
// it, and only one of the two is a real guarantee.
func TestListSessionFamiliesJudgesOnlyTheNewestGeneration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	pool := assertionPool(ctx, t, dsn)

	user := mustCreateUser(ctx, t, store, newUser("fencer"))
	first := mustCreateSession(ctx, t, store, user.ID,
		deviceTokens("f-acc", "f-ref", "fenced-device", "203.0.113.70"))

	// Rotating leaves generation 1 used but neither revoked nor expired.
	if _, outcome, err := store.RotateSession(ctx, hashOf("f-ref"),
		deviceTokens("f2-acc", "f2-ref", "fenced-device", "203.0.113.70")); err != nil ||
		outcome != storage.RotateOutcomeRotated {
		t.Fatalf("rotate the fenced family: outcome %v, err %v", outcome, err)
	}

	// Revoke ONLY the newest generation. No storage method does this — they
	// all revoke whole families, which is the correct behaviour and exactly
	// why the fence is otherwise untested.
	tag, err := pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now()
		  WHERE family_id = $1
		    AND created_at = (SELECT max(created_at) FROM sessions WHERE family_id = $1)`,
		first.FamilyID,
	)
	if err != nil {
		t.Fatalf("revoke the newest generation: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("revoked %d generations, want exactly the newest one", tag.RowsAffected())
	}

	total, revoked := familyRowCounts(ctx, t, pool, first.FamilyID)
	if total != 2 || revoked != 1 {
		t.Fatalf("fixture is %d generations with %d revoked, want 2 with 1", total, revoked)
	}

	for _, fam := range mustListFamilies(ctx, t, store, user.ID, uuid.Nil) {
		if fam.FamilyID == first.FamilyID {
			t.Fatal("a family whose newest generation is revoked is still listed as signed in; " +
				"the liveness filter was folded into the per-family pick and resurrected " +
				"the family from its older, unrevoked generation")
		}
	}
}

// TestRevokeUserFamilyIntegration pins remote sign-out: every generation
// dies, repeating it is harmless, and another account's family is neither
// found nor touched.
func TestRevokeUserFamilyIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	pool := assertionPool(ctx, t, dsn)

	user := mustCreateUser(ctx, t, store, newUser("revoker"))
	victim := mustCreateUser(ctx, t, store, newUser("revokervictim"))

	// A family with two generations: the first was rotated away, the second
	// is live. Revocation has to reach both.
	target := mustCreateSession(ctx, t, store, user.ID, deviceTokens("t-acc", "t-ref", "target", "203.0.113.40"))
	if _, outcome, err := store.RotateSession(ctx, hashOf("t-ref"), tokensFor("t2-acc", "t2-ref")); err != nil ||
		outcome != storage.RotateOutcomeRotated {
		t.Fatalf("rotate target: outcome %v, err %v", outcome, err)
	}

	t.Run("revokes every generation of the family", func(t *testing.T) {
		if err := store.RevokeUserFamily(ctx, user.ID, target.FamilyID); err != nil {
			t.Fatalf("RevokeUserFamily: %v", err)
		}

		total, revoked := familyRowCounts(ctx, t, pool, target.FamilyID)
		if total != 2 || revoked != total {
			t.Errorf("%d of %d generations revoked, want all %d", revoked, total, total)
		}
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("t2-acc")); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("the live access token survived revocation: %v", err)
		}
		_, outcome, err := store.RotateSession(ctx, hashOf("t2-ref"), tokensFor("t3-acc", "t3-ref"))
		if err != nil {
			t.Fatalf("RotateSession after revocation: %v", err)
		}
		if outcome != storage.RotateOutcomeInvalid {
			t.Errorf("a revoked family still rotates: outcome %v, want invalid", outcome)
		}
	})

	t.Run("revoking again succeeds and changes nothing", func(t *testing.T) {
		if err := store.RevokeUserFamily(ctx, user.ID, target.FamilyID); err != nil {
			t.Fatalf("second RevokeUserFamily: %v", err)
		}
		total, revoked := familyRowCounts(ctx, t, pool, target.FamilyID)
		if total != 2 || revoked != 2 {
			t.Errorf("after re-revoking: %d of %d generations revoked, want 2 of 2", revoked, total)
		}
	})

	t.Run("another account's family is not found and is not revoked", func(t *testing.T) {
		victimSess := mustCreateSession(ctx, t, store, victim.ID,
			deviceTokens("v-acc", "v-ref", "victim", "203.0.113.50"))

		err := store.RevokeUserFamily(ctx, user.ID, victimSess.FamilyID)
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound for a family the caller does not own", err)
		}

		// The status code alone would hide the real bug: assert the victim's
		// session is untouched.
		if _, revoked := familyRowCounts(ctx, t, pool, victimSess.FamilyID); revoked != 0 {
			t.Errorf("%d generations of the victim's family were revoked, want 0", revoked)
		}
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("v-acc")); err != nil {
			t.Errorf("the victim's session stopped working: %v", err)
		}
	})

	t.Run("an unknown family is not found", func(t *testing.T) {
		if err := store.RevokeUserFamily(ctx, user.ID, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound for a family that does not exist", err)
		}
	})
}

// TestRevokeOtherFamiliesIntegration pins "sign out everywhere else": the
// caller's own family survives, every other family of theirs dies, and no
// other account is affected.
func TestRevokeOtherFamiliesIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	user := mustCreateUser(ctx, t, store, newUser("sweeper"))
	bystander := mustCreateUser(ctx, t, store, newUser("sweeperbystander"))

	keep := mustCreateSession(ctx, t, store, user.ID, deviceTokens("k-acc", "k-ref", "keep", "203.0.113.60"))
	mustCreateSession(ctx, t, store, user.ID, deviceTokens("o1-acc", "o1-ref", "other-1", "203.0.113.61"))
	mustCreateSession(ctx, t, store, user.ID, deviceTokens("o2-acc", "o2-ref", "other-2", "203.0.113.62"))
	mustCreateSession(ctx, t, store, bystander.ID, deviceTokens("by-acc", "by-ref", "bystander", "203.0.113.63"))

	if err := store.RevokeOtherFamilies(ctx, user.ID, keep.FamilyID); err != nil {
		t.Fatalf("RevokeOtherFamilies: %v", err)
	}

	if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("k-acc")); err != nil {
		t.Errorf("the calling session died: %v", err)
	}
	for _, token := range []string{"o1-acc", "o2-acc"} {
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf(token)); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("session %s survived sign-out-everywhere-else: %v", token, err)
		}
	}
	if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("by-acc")); err != nil {
		t.Errorf("an unrelated account's session died: %v", err)
	}

	want := []uuid.UUID{keep.FamilyID}
	if got := familyIDs(mustListFamilies(ctx, t, store, user.ID, keep.FamilyID)); !slices.Equal(got, want) {
		t.Errorf("list after revoking the rest:\n got %v\nwant %v", got, want)
	}

	// Repeating it with nothing else signed in is still success.
	if err := store.RevokeOtherFamilies(ctx, user.ID, keep.FamilyID); err != nil {
		t.Errorf("RevokeOtherFamilies with nothing left to revoke: %v", err)
	}
	if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("k-acc")); err != nil {
		t.Errorf("the calling session died on the second sweep: %v", err)
	}
}
