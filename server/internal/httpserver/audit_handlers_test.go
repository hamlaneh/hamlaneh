package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

const auditFixturePassword = "an audit fixture password"

// auditKey is this test file's chain key. Nothing in the repository ships a
// working default (CLAUDE.md); this one exists only here.
const auditKey = "audit handler test key, 32+ bytes long"

// auditFixture is a signed-in admin, a server that can verify its own log,
// and the recorder that fills it.
type auditFixture struct {
	store   testdb.Store
	dsn     string
	handler http.Handler
	chain   *audit.Chain
	rec     *audit.Recorder
	admin   storage.User
	cookies sessionCookies
}

func newAuditFixture(t *testing.T) auditFixture {
	t.Helper()

	store, dsn := testdb.New(t)
	chain, err := audit.New([]byte(auditKey))
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	handler := httpserver.Handler(store, httpserver.WithAuditChain(chain))

	admin, err := store.CreateUser(context.Background(), storage.NewUser{
		Username:     "auditadmin",
		DisplayName:  "The Admin",
		PasswordHash: password.Hash(auditFixturePassword),
		Locale:       "en",
		IsAdmin:      true,
	})
	if err != nil {
		t.Fatalf("create the admin fixture: %v", err)
	}

	return auditFixture{
		store: store, dsn: dsn, handler: handler, chain: chain,
		rec:     audit.NewRecorder(chain, store),
		admin:   admin,
		cookies: login(t, handler, "auditadmin", auditFixturePassword),
	}
}

// record writes one entry as the fixture admin.
func (f auditFixture) record(t *testing.T, action string) {
	t.Helper()
	f.rec.Record(context.Background(), audit.Record{
		Action:      action,
		ActorID:     &f.admin.ID,
		TargetID:    &f.admin.ID,
		TargetLabel: f.admin.Username,
		Detail:      map[string]any{"note": "recorded by the handler test"},
		IP:          netip.MustParseAddr("192.0.2.7"),
	})
}

// page reads one page of the log through the real stack.
func (f auditFixture) page(t *testing.T, query string) api.AuditPage {
	t.Helper()
	rec := doHandler(t, f.handler, request(http.MethodGet, "/api/v1/admin/audit"+query, "", withSession(f.cookies)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET the audit log%s: status %d (body %s)", query, rec.Code, rec.Body.String())
	}
	var page api.AuditPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode the page: %v (body %s)", err, rec.Body.String())
	}
	return page
}

func TestAuditLogPageIntegration(t *testing.T) {
	t.Parallel()

	f := newAuditFixture(t)
	for i := range 4 {
		f.record(t, fmt.Sprintf("test.action%d", i%2))
	}

	t.Run("newest first, verified, fully populated", func(t *testing.T) {
		page := f.page(t, "")
		if !page.ChainValid {
			t.Error("chain_valid is false on a log this server just wrote")
		}
		if len(page.Entries) != 4 {
			t.Fatalf("page has %d entries, want 4", len(page.Entries))
		}
		if page.Entries[0].Action != "test.action1" {
			t.Errorf("first entry is %q, want the newest", page.Entries[0].Action)
		}
		if page.NextCursor != nil {
			t.Error("a page holding everything handed out a next cursor")
		}

		e := page.Entries[0]
		switch {
		case e.Actor == nil || e.Actor.Username != "auditadmin":
			t.Errorf("actor = %+v, want auditadmin", e.Actor)
		case e.TargetId == nil || *e.TargetId != f.admin.ID:
			t.Errorf("target = %v, want the admin", e.TargetId)
		case e.TargetLabel == nil || *e.TargetLabel != "auditadmin":
			t.Errorf("target label = %v, want auditadmin", e.TargetLabel)
		case e.Ip == nil || *e.Ip != "192.0.2.7":
			t.Errorf("address = %v, want 192.0.2.7", e.Ip)
		case e.Detail == nil || (*e.Detail)["note"] != "recorded by the handler test":
			t.Errorf("detail = %v, want the note it was recorded with", e.Detail)
		case e.Id == uuid.Nil || e.OccurredAt.IsZero():
			t.Error("entry is missing its id or its timestamp")
		}
	})

	t.Run("pages with a cursor", func(t *testing.T) {
		first := f.page(t, "?limit=2")
		if len(first.Entries) != 2 || first.NextCursor == nil {
			t.Fatalf("first page: %d entries, cursor %v", len(first.Entries), first.NextCursor)
		}
		second := f.page(t, "?limit=2&cursor="+*first.NextCursor)
		if len(second.Entries) != 2 {
			t.Fatalf("second page: %d entries, want 2", len(second.Entries))
		}
		if second.Entries[0].Id == first.Entries[1].Id {
			t.Error("the cursor repeated an entry")
		}
		if !first.ChainValid || !second.ChainValid {
			t.Error("a page boundary reported a broken chain")
		}
	})

	t.Run("filters", func(t *testing.T) {
		byAction := f.page(t, "?action=test.action0")
		if len(byAction.Entries) != 2 {
			t.Fatalf("action filter returned %d entries, want 2", len(byAction.Entries))
		}
		if !byAction.ChainValid {
			t.Error("a filtered page reported a broken chain")
		}
		byActor := f.page(t, "?actor_id="+f.admin.ID.String())
		if len(byActor.Entries) != 4 {
			t.Errorf("actor filter returned %d entries, want 4", len(byActor.Entries))
		}
		none := f.page(t, "?actor_id="+uuid.New().String())
		if len(none.Entries) != 0 {
			t.Errorf("filtering by a stranger returned %d entries, want none", len(none.Entries))
		}
		if !none.ChainValid {
			t.Error("an empty page is not a broken chain")
		}
	})

	t.Run("refuses a bad query", func(t *testing.T) {
		for _, query := range []string{"?limit=0", "?limit=101", "?cursor=not-a-cursor", "?action=" + longAction()} {
			rec := doHandler(t, f.handler,
				request(http.MethodGet, "/api/v1/admin/audit"+query, "", withSession(f.cookies)))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("GET %s: status %d, want 400", query, rec.Code)
			}
		}
	})
}

// TestAuditPageReportsTamperedRows is the endpoint's half of the feature: a
// row edited behind the server's back comes back with chain_valid false, and
// the entries still come back — hiding them would hide the evidence.
func TestAuditPageReportsTamperedRows(t *testing.T) {
	t.Parallel()

	f := newAuditFixture(t)
	for i := range 3 {
		f.record(t, fmt.Sprintf("test.action%d", i))
	}
	if !f.page(t, "").ChainValid {
		t.Fatal("chain_valid is false before anything was tampered with")
	}

	conn, err := pgx.Connect(context.Background(), f.dsn)
	if err != nil {
		t.Fatalf("connect to the scratch database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	if _, err = conn.Exec(context.Background(),
		`UPDATE audit_entries SET action = 'test.rewritten' WHERE seq = 2`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	page := f.page(t, "")
	if page.ChainValid {
		t.Error("chain_valid is true after a row was edited in the database")
	}
	if len(page.Entries) != 3 {
		t.Errorf("page has %d entries after the tamper, want all 3 still listed", len(page.Entries))
	}
}

// TestAuditPageReportsDeletedRows: a deleted row leaves every remaining seal
// intact, so this is the case only the linkage across the page can catch.
func TestAuditPageReportsDeletedRows(t *testing.T) {
	t.Parallel()

	f := newAuditFixture(t)
	for i := range 3 {
		f.record(t, fmt.Sprintf("test.action%d", i))
	}

	conn, err := pgx.Connect(context.Background(), f.dsn)
	if err != nil {
		t.Fatalf("connect to the scratch database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	if _, err = conn.Exec(context.Background(), `DELETE FROM audit_entries WHERE seq = 2`); err != nil {
		t.Fatalf("delete an entry: %v", err)
	}

	page := f.page(t, "")
	if page.ChainValid {
		t.Error("chain_valid is true after a row was deleted from the database")
	}
	if len(page.Entries) != 2 {
		t.Errorf("page has %d entries after the delete, want the 2 that remain", len(page.Entries))
	}
}

// TestAuditPageWithoutAChainRefuses: a server that cannot verify what it
// reads must not answer with a chain_valid it did not check.
func TestAuditPageWithoutAChainRefuses(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	handler := httpserver.Handler(store)
	if _, err := store.CreateUser(context.Background(), storage.NewUser{
		Username: "nochainadmin", PasswordHash: password.Hash(auditFixturePassword),
		Locale: "en", IsAdmin: true,
	}); err != nil {
		t.Fatalf("create the admin fixture: %v", err)
	}
	cookies := login(t, handler, "nochainadmin", auditFixturePassword)

	rec := doHandler(t, handler, request(http.MethodGet, "/api/v1/admin/audit", "", withSession(cookies)))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d with no chain configured, want 500", rec.Code)
	}
}

func longAction() string {
	action := make([]byte, 65)
	for i := range action {
		action[i] = 'a'
	}
	return string(action)
}
