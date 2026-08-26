package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// redemption builds the account one invitation would create.
func redemption(name string) storage.NewUser {
	return storage.NewUser{
		Username:     name,
		DisplayName:  "Display " + name,
		PasswordHash: "fake-hash-" + name,
		Locale:       "en",
	}
}

const inviteTTL = 24 * time.Hour

func TestInvitesIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	host := mustCreateUser(ctx, t, store, adminUser("invitehost"))

	mustCreate := func(t *testing.T, token, note string, ttl time.Duration) storage.Invite {
		t.Helper()
		inv, err := store.CreateInvite(ctx, host.ID, hashOf(token), note, ttl)
		if err != nil {
			t.Fatalf("CreateInvite(%s): %v", token, err)
		}
		return inv
	}

	t.Run("create returns the row with its creator", func(t *testing.T) {
		inv := mustCreate(t, "created-1", "for the new designer", inviteTTL)
		if inv.ID == uuid.Nil {
			t.Error("no id assigned")
		}
		if inv.Note != "for the new designer" {
			t.Errorf("note = %q", inv.Note)
		}
		if inv.CreatedBy.ID != host.ID || inv.CreatedBy.Username != host.Username {
			t.Errorf("created_by = %+v, want %s/%s", inv.CreatedBy, host.ID, host.Username)
		}
		if !inv.ExpiresAt.After(inv.CreatedAt) {
			t.Error("expiry is not after creation")
		}
	})

	t.Run("the raw token is never stored", func(t *testing.T) {
		mustCreate(t, "secret-token-value", "", inviteTTL)

		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() {
			if closeErr := conn.Close(ctx); closeErr != nil {
				t.Errorf("close: %v", closeErr)
			}
		}()

		var n int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM invites WHERE token_hash = $1::bytea`,
			[]byte("secret-token-value"),
		).Scan(&n); err != nil {
			t.Fatalf("count raw tokens: %v", err)
		}
		if n != 0 {
			t.Errorf("%d invites store the raw token; only its hash may be stored", n)
		}
	})

	t.Run("preview resolves a live token and nothing else", func(t *testing.T) {
		mustCreate(t, "live-token", "", inviteTTL)
		if _, err := store.OpenInviteByTokenHash(ctx, hashOf("live-token")); err != nil {
			t.Errorf("OpenInviteByTokenHash on a live token: %v", err)
		}

		mustCreate(t, "expired-token", "", -time.Hour)
		revoked := mustCreate(t, "revoked-token", "", inviteTTL)
		if err := store.RevokeInvite(ctx, revoked.ID); err != nil {
			t.Fatalf("RevokeInvite: %v", err)
		}
		mustCreate(t, "spent-token", "", inviteTTL)
		if _, err := store.RedeemInvite(ctx, hashOf("spent-token"), redemption("spentuser")); err != nil {
			t.Fatalf("RedeemInvite: %v", err)
		}

		// Unknown, expired, revoked and spent are ONE answer. The storage
		// layer collapses them so nothing above it can leak the difference.
		for _, token := range []string{"never-issued", "expired-token", "revoked-token", "spent-token"} {
			if _, err := store.OpenInviteByTokenHash(ctx, hashOf(token)); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("OpenInviteByTokenHash(%s) = %v, want ErrNotFound", token, err)
			}
			if _, err := store.RedeemInvite(ctx, hashOf(token), redemption("nobody")); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("RedeemInvite(%s) = %v, want ErrNotFound", token, err)
			}
		}
	})

	t.Run("revocation is idempotent", func(t *testing.T) {
		inv := mustCreate(t, "twice-revoked", "", inviteTTL)
		for i := range 2 {
			if err := store.RevokeInvite(ctx, inv.ID); err != nil {
				t.Fatalf("RevokeInvite call %d: %v", i+1, err)
			}
		}
		if err := store.RevokeInvite(ctx, uuid.New()); err != nil {
			t.Errorf("revoking an invitation that never existed: %v", err)
		}
	})

	t.Run("redemption creates the account and consumes the link", func(t *testing.T) {
		mustCreate(t, "redeem-once", "", inviteTTL)
		created, err := store.RedeemInvite(ctx, hashOf("redeem-once"), redemption("newcomer"))
		if err != nil {
			t.Fatalf("RedeemInvite: %v", err)
		}
		if created.Username != "newcomer" || !created.IsActive || created.IsAdmin {
			t.Errorf("redeemed account = %+v, want an active non-admin newcomer", created)
		}
		if _, err := store.RedeemInvite(ctx, hashOf("redeem-once"), redemption("secondcomer")); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("second redemption = %v, want ErrNotFound", err)
		}
	})

	t.Run("a username conflict does not burn the link", func(t *testing.T) {
		mustCreate(t, "conflict-token", "", inviteTTL)
		if _, err := store.RedeemInvite(ctx, hashOf("conflict-token"), redemption("invitehost")); !errors.Is(err, storage.ErrUsernameTaken) {
			t.Fatalf("redeeming with a taken username = %v, want ErrUsernameTaken", err)
		}
		// The whole transaction rolled back, so the invitation is still live
		// for the next try — which is the only behaviour that lets somebody
		// pick a different name.
		if _, err := store.RedeemInvite(ctx, hashOf("conflict-token"), redemption("secondchoice")); err != nil {
			t.Errorf("retry after a name conflict: %v", err)
		}
	})

	t.Run("the list shows only what can still be redeemed", func(t *testing.T) {
		invites, err := store.ListOpenInvites(ctx, storage.ListInvitesParams{Limit: 100})
		if err != nil {
			t.Fatalf("ListOpenInvites: %v", err)
		}
		open := map[uuid.UUID]bool{}
		for _, inv := range invites {
			open[inv.ID] = true
			if !inv.ExpiresAt.After(time.Now()) {
				t.Errorf("invite %s is listed but already expired", inv.ID)
			}
		}
		for i := 1; i < len(invites); i++ {
			if invites[i].ExpiresAt.Before(invites[i-1].ExpiresAt) {
				t.Error("the list is not ordered by soonest expiry first")
			}
		}
	})

	t.Run("pagination walks every open invite exactly once", func(t *testing.T) {
		// A page of two at a time over whatever this database holds.
		seen := map[uuid.UUID]int{}
		var after *storage.InviteCursor
		for range 50 {
			page, err := store.ListOpenInvites(ctx, storage.ListInvitesParams{After: after, Limit: 2})
			if err != nil {
				t.Fatalf("ListOpenInvites: %v", err)
			}
			if len(page) == 0 {
				break
			}
			for _, inv := range page {
				seen[inv.ID]++
			}
			last := page[len(page)-1]
			after = &storage.InviteCursor{ExpiresAt: last.ExpiresAt, ID: last.ID}
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("invite %s returned %d times across pages, want 1", id, n)
			}
		}
	})
}

// TestRedeemInviteRaceProducesOneAccount is the promise the contract makes
// about two people opening the same link: one account and one honest refusal.
//
// The transaction locks the invitation row before it creates anything, so the
// loser blocks, re-reads the row it waited for under READ COMMITTED, and
// finds it spent. Without that lock both racers would insert.
func TestRedeemInviteRaceProducesOneAccount(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	host := mustCreateUser(ctx, t, store, adminUser("racehost"))

	if _, err := store.CreateInvite(ctx, host.ID, hashOf("contested"), "", inviteTTL); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	const racers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accounts []storage.User
		refusals int
	)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			u, err := store.RedeemInvite(ctx, hashOf("contested"),
				redemption(fmt.Sprintf("racer%d", i)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accounts = append(accounts, u)
			case errors.Is(err, storage.ErrNotFound):
				refusals++
			default:
				t.Errorf("racer %d failed for an unexpected reason: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(accounts) != 1 {
		t.Fatalf("%d accounts created from one link, want exactly 1", len(accounts))
	}
	if refusals != racers-1 {
		t.Errorf("%d honest refusals, want %d", refusals, racers-1)
	}

	// And the account that exists is the one the invitation records.
	if _, err := store.UserByIdentifier(ctx, accounts[0].Username); err != nil {
		t.Errorf("the winning account is not readable back: %v", err)
	}
	if _, err := store.OpenInviteByTokenHash(ctx, hashOf("contested")); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the link is still live after being redeemed: %v", err)
	}
}
