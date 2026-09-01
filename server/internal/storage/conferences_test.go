package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

func TestConferencesIntegration(t *testing.T) {
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()
	host := mustCreateUser(ctx, t, store, newUser("confhost"))

	mustCreate := func(t *testing.T, token, title string, expiresAt *time.Time) storage.Conference {
		t.Helper()
		conf, err := store.CreateConference(ctx, host.ID, hashOf(token), title, expiresAt)
		if err != nil {
			t.Fatalf("CreateConference(%s): %v", token, err)
		}
		return conf
	}

	t.Run("create returns the row with its creator", func(t *testing.T) {
		conf := mustCreate(t, "conf-created-1", "Weekly sync", nil)
		if conf.ID == uuid.Nil {
			t.Error("no id assigned")
		}
		if conf.Title != "Weekly sync" {
			t.Errorf("title = %q", conf.Title)
		}
		if conf.CreatedBy == nil || conf.CreatedBy.ID != host.ID || conf.CreatedBy.Username != host.Username {
			t.Errorf("created_by = %+v, want %s/%s", conf.CreatedBy, host.ID, host.Username)
		}
		if conf.ExpiresAt != nil {
			t.Errorf("expires_at = %v, want a link that does not expire", conf.ExpiresAt)
		}
	})

	t.Run("the raw link is never stored", func(t *testing.T) {
		mustCreate(t, "a-secret-conference-link", "", nil)

		var n int
		if err := raw.QueryRow(ctx,
			`SELECT count(*) FROM conferences WHERE link_token_hash = ?`,
			[]byte("a-secret-conference-link"),
		).Scan(&n); err != nil {
			t.Fatalf("count raw links: %v", err)
		}
		if n != 0 {
			t.Errorf("%d conferences store the raw link; only its digest may be stored", n)
		}
	})

	// The property the whole surface rests on: a visitor learns whether their
	// link works, never why it does not. Three failures, one answer.
	t.Run("unknown, expired and revoked are one answer", func(t *testing.T) {
		expired := time.Now().Add(-time.Minute)
		mustCreate(t, "conf-expired", "", &expired)
		revoked := mustCreate(t, "conf-revoked", "", nil)
		if err := store.RevokeConference(ctx, revoked.ID); err != nil {
			t.Fatalf("RevokeConference: %v", err)
		}

		for name, token := range map[string]string{
			"unknown": "conf-never-issued",
			"expired": "conf-expired",
			"revoked": "conf-revoked",
		} {
			_, err := store.LiveConferenceByTokenHash(ctx, hashOf(token))
			if !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("%s link resolved to %v, want ErrNotFound", name, err)
			}
		}
	})

	t.Run("a live link resolves", func(t *testing.T) {
		want := mustCreate(t, "conf-live", "Standing meeting", nil)
		got, err := store.LiveConferenceByTokenHash(ctx, hashOf("conf-live"))
		if err != nil {
			t.Fatalf("LiveConferenceByTokenHash on a live link: %v", err)
		}
		if got.ID != want.ID || got.Title != want.Title {
			t.Errorf("got %+v, want the created conference %+v", got, want)
		}
	})

	// An expiry that has not arrived yet must not read as expired: the
	// predicate is a comparison, and getting its direction wrong would kill
	// every dated link the moment it was made.
	t.Run("a link with a future expiry still resolves", func(t *testing.T) {
		future := time.Now().Add(time.Hour)
		mustCreate(t, "conf-future", "", &future)
		if _, err := store.LiveConferenceByTokenHash(ctx, hashOf("conf-future")); err != nil {
			t.Errorf("a link expiring in an hour did not resolve: %v", err)
		}
	})

	t.Run("revoking twice reports the second as nothing", func(t *testing.T) {
		conf := mustCreate(t, "conf-twice", "", nil)
		if err := store.RevokeConference(ctx, conf.ID); err != nil {
			t.Fatalf("first revoke: %v", err)
		}
		if err := store.RevokeConference(ctx, conf.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("second revoke = %v, want ErrNotFound", err)
		}
		if err := store.RevokeConference(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("revoking an id that names nothing = %v, want ErrNotFound", err)
		}
	})

	// The two ways a conference id can fail to be actionable are one answer,
	// for the same reason the three link failures are.
	t.Run("a revoked conference is gone to the id lookup too", func(t *testing.T) {
		conf := mustCreate(t, "conf-byid-revoked", "", nil)
		if _, err := store.ConferenceByID(ctx, conf.ID); err != nil {
			t.Fatalf("ConferenceByID on a live conference: %v", err)
		}
		if err := store.RevokeConference(ctx, conf.ID); err != nil {
			t.Fatalf("RevokeConference: %v", err)
		}
		if _, err := store.ConferenceByID(ctx, conf.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("ConferenceByID on a revoked conference = %v, want ErrNotFound", err)
		}
		if _, err := store.ConferenceByID(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("ConferenceByID on an unknown id = %v, want ErrNotFound", err)
		}
	})

	// An expired link is dead to a visitor and alive to its owner: it is
	// still a row they may want to see and take off the list.
	t.Run("an expired conference is still the owner's to see and revoke", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		conf := mustCreate(t, "conf-owner-expired", "", &past)
		if _, err := store.ConferenceByID(ctx, conf.ID); err != nil {
			t.Errorf("ConferenceByID on an expired conference: %v", err)
		}
		if err := store.RevokeConference(ctx, conf.ID); err != nil {
			t.Errorf("revoking an expired conference: %v", err)
		}
	})
}

// TestConferenceListing runs on its own database: the list is a whole-table
// read, so a neighbouring test's rows would be its rows.
func TestConferenceListing(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	mine := mustCreateUser(ctx, t, store, newUser("listmine"))
	theirs := mustCreateUser(ctx, t, store, newUser("listtheirs"))

	ownID, err := store.CreateConference(ctx, mine.ID, hashOf("list-own"), "Mine", nil)
	if err != nil {
		t.Fatalf("create own conference: %v", err)
	}
	otherID, err := store.CreateConference(ctx, theirs.ID, hashOf("list-other"), "Theirs", nil)
	if err != nil {
		t.Fatalf("create other conference: %v", err)
	}
	goneID, err := store.CreateConference(ctx, mine.ID, hashOf("list-gone"), "Revoked", nil)
	if err != nil {
		t.Fatalf("create revoked conference: %v", err)
	}
	if err := store.RevokeConference(ctx, goneID.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	ids := func(t *testing.T, confs []storage.Conference) map[uuid.UUID]bool {
		t.Helper()
		out := map[uuid.UUID]bool{}
		for _, c := range confs {
			out[c.ID] = true
		}
		return out
	}

	t.Run("mine is only mine, and never the revoked one", func(t *testing.T) {
		confs, err := store.ListConferences(ctx, mine.ID, false)
		if err != nil {
			t.Fatalf("ListConferences: %v", err)
		}
		got := ids(t, confs)
		if !got[ownID.ID] {
			t.Error("my own conference is missing from my list")
		}
		if got[otherID.ID] {
			t.Error("somebody else's conference is in my list")
		}
		if got[goneID.ID] {
			t.Error("a revoked conference is still listed")
		}
	})

	t.Run("all is everybody's", func(t *testing.T) {
		confs, err := store.ListConferences(ctx, mine.ID, true)
		if err != nil {
			t.Fatalf("ListConferences(all): %v", err)
		}
		got := ids(t, confs)
		if !got[ownID.ID] || !got[otherID.ID] {
			t.Errorf("the instance-wide list is missing rows: %+v", confs)
		}
		if got[goneID.ID] {
			t.Error("a revoked conference is in the instance-wide list")
		}
	})
}

// TestConferenceOutlivesItsOwner is the case ADR 005 and migration 0016 both
// name: created_by is SET NULL, so the row survives the account, and an
// administrator must still be able to reach it. A nil CreatedBy — not a zero
// one — is what tells "the account is gone" from "created by nobody".
func TestConferenceOutlivesItsOwner(t *testing.T) {
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()
	owner := mustCreateUser(ctx, t, store, newUser("departing"))

	conf, err := store.CreateConference(ctx, owner.ID, hashOf("orphan-link"), "Standing", nil)
	if err != nil {
		t.Fatalf("CreateConference: %v", err)
	}

	// Accounts are deactivated rather than deleted by this server; the row is
	// deleted directly here because the nullable column is what is under
	// test, and nothing in the API can produce that state on demand.
	raw.Exec(ctx, t, `DELETE FROM users WHERE id = ?`, owner.ID)

	orphan, err := store.ConferenceByID(ctx, conf.ID)
	if err != nil {
		t.Fatalf("ConferenceByID after the owner was deleted: %v", err)
	}
	if orphan.CreatedBy != nil {
		t.Errorf("created_by = %+v, want nil now the account is gone", orphan.CreatedBy)
	}
	if _, err := store.LiveConferenceByTokenHash(ctx, hashOf("orphan-link")); err != nil {
		t.Errorf("the link stopped working when its owner left: %v", err)
	}
	if err := store.RevokeConference(ctx, conf.ID); err != nil {
		t.Errorf("an ownerless conference could not be revoked: %v", err)
	}
}
