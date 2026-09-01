package storage_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The organisation encryption mode (migration 0018, ADR 011): what it is,
// what it counts, and — the one that matters — what it leaves alone.

func TestEncryptionModeRoundTripIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := testdb.New(t)

	mode, err := store.EncryptionMode(ctx)
	if err != nil {
		t.Fatalf("EncryptionMode: %v", err)
	}
	if mode != storage.EncryptionModeStrict {
		t.Fatalf("a fresh instance reads %q, want strict", mode)
	}

	// The compliance branch is written and reachable here even though the
	// API refuses to select it: the write path must already be correct the
	// day the gate lifts (ADR 011 decision 3).
	for _, want := range []string{storage.EncryptionModeCompliance, storage.EncryptionModeStrict} {
		settings, setErr := store.SetEncryptionMode(ctx, want)
		if setErr != nil {
			t.Fatalf("SetEncryptionMode(%s): %v", want, setErr)
		}
		if settings.EncryptionMode != want {
			t.Errorf("the write answered %q, want %q", settings.EncryptionMode, want)
		}
		got, readErr := store.EncryptionMode(ctx)
		if readErr != nil {
			t.Fatalf("EncryptionMode: %v", readErr)
		}
		if got != want {
			t.Errorf("stored mode = %q, want %q", got, want)
		}
	}

	// The column refuses a third mode; §6.4 named two.
	if _, err := store.SetEncryptionMode(ctx, "both"); err == nil {
		t.Error("SetEncryptionMode accepted a mode outside the CHECK")
	}
}

// TestSettingsPatchCannotMoveTheModeIntegration pins the structural half of
// "the mode is written only through its own endpoint": the field-by-field
// save has no field for it, so no patch can flip it in passing.
func TestSettingsPatchCannotMoveTheModeIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := testdb.New(t)

	if _, err := store.SetEncryptionMode(ctx, storage.EncryptionModeCompliance); err != nil {
		t.Fatalf("SetEncryptionMode: %v", err)
	}
	patched, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{
		OrgName: ptr("Nest"), RequireTotp: ptr(true),
	})
	if err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	if patched.EncryptionMode != storage.EncryptionModeCompliance {
		t.Errorf("a settings patch moved the mode to %q", patched.EncryptionMode)
	}
}

// TestConversationTotalsIntegration pins the two standing totals, and that a
// mode switch moves neither of them. Both are published rather than one
// "outside the current mode" count because each switch dialog names what
// will be outside the mode being CHOSEN — the other set in each direction.
func TestConversationTotalsIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := testdb.New(t)

	creator := mustCreateUser(ctx, t, store, newUser("totals"))
	settings, err := store.OrgSettings(ctx)
	if err != nil {
		t.Fatalf("OrgSettings: %v", err)
	}
	if settings.EncryptedConversations != 0 || settings.PlaintextConversations != 0 {
		t.Fatalf("an empty instance counts %d encrypted and %d plaintext conversations, want none",
			settings.EncryptedConversations, settings.PlaintextConversations)
	}

	for _, ch := range []struct {
		slug string
		e2ee bool
	}{
		{"totals-plain-one", false},
		{"totals-plain-two", false},
		{"totals-encrypted", true},
	} {
		mustCreateChannel(ctx, t, store, storage.NewChannel{
			Kind: storage.ChannelKindPrivate, Slug: ch.slug, E2EE: ch.e2ee, CreatedBy: creator.ID,
		})
	}

	// The same two numbers in either mode: nothing is ever converted, so a
	// switch cannot move them — decision 2 restated as data.
	for _, mode := range []string{storage.EncryptionModeStrict, storage.EncryptionModeCompliance} {
		t.Run(mode, func(t *testing.T) {
			written, setErr := store.SetEncryptionMode(ctx, mode)
			if setErr != nil {
				t.Fatalf("SetEncryptionMode: %v", setErr)
			}
			read, readErr := store.OrgSettings(ctx)
			if readErr != nil {
				t.Fatalf("OrgSettings: %v", readErr)
			}
			for _, got := range []storage.OrgSettings{written, read} {
				if got.EncryptedConversations != 1 || got.PlaintextConversations != 2 {
					t.Errorf("under %s the totals are %d encrypted / %d plaintext, want 1 / 2",
						mode, got.EncryptedConversations, got.PlaintextConversations)
				}
			}
		})
	}
}

// TestModeSwitchTouchesNoConversationIntegration is Phase 3 gate item 3:
// switching modes cannot silently decrypt or expose history.
//
// It is the drill ADR 011 decision 2 specifies, automated: a canary inside an
// encrypted conversation and a plaintext control beside it, the mode switched
// in both directions, and then the whole database scanned. What must hold
// after every switch is that the encrypted canary is nowhere in the stored
// bytes, the control canary still is (which is what proves the scan can see
// anything at all), and every channel row and every message row is
// byte-identical to what it was before.
//
// The assertion is deliberately made on whole rows rendered as text rather
// than on the e2ee flag alone: a mode switch that quietly rewrote a
// ciphertext, a timestamp or an epoch would be just as much a silent
// mode-switch as one that flipped the flag.
func TestModeSwitchTouchesNoConversationIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, raw := testdb.New(t)

	// The plaintext the client encrypted. It never reaches the server, and
	// no switch may make it appear.
	const secretCanary = "the merger closes on friday"
	// The control. It was stored server-readable, so the scan MUST find it —
	// a scan that found nothing would pass this test while proving nothing.
	const controlCanary = "the picnic is on saturday"

	author := mustCreateUser(ctx, t, store, newUser("gatethree"))
	peer := mustCreateUser(ctx, t, store, newUser("gatethreepeer"))

	encrypted := mustCreateChannel(ctx, t, store, storage.NewChannel{
		Kind: storage.ChannelKindPrivate, Slug: "gate3-encrypted", E2EE: true, CreatedBy: author.ID,
	})
	plain := mustCreateChannel(ctx, t, store, storage.NewChannel{
		Kind: storage.ChannelKindPrivate, Slug: "gate3-plain", CreatedBy: author.ID,
	})
	dm := mustOpenDM(ctx, t, store, author.ID, peer.ID)

	if _, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID: encrypted.ID, AuthorID: author.ID, ClientMsgID: uuid.New(),
		Mls: &storage.MessageMls{Epoch: 4, Ciphertext: []byte("opaque-envelope-bytes")},
	}); err != nil {
		t.Fatalf("CreateMessage (encrypted): %v", err)
	}
	for _, target := range []uuid.UUID{plain.ID, dm.ID} {
		if _, _, err := store.CreateMessage(ctx, storage.NewMessage{
			ChannelID: target, AuthorID: author.ID, ClientMsgID: uuid.New(),
			Content: controlCanary,
		}); err != nil {
			t.Fatalf("CreateMessage (plaintext): %v", err)
		}
	}

	assertCanaries := func(t *testing.T, when string) {
		t.Helper()
		if hits := scanDatabaseFor(ctx, t, raw, controlCanary); len(hits) == 0 {
			t.Fatalf("%s: the control canary is nowhere in the database; this drill proves nothing until the scan can see plaintext", when)
		}
		if hits := scanDatabaseFor(ctx, t, raw, secretCanary); len(hits) > 0 {
			t.Errorf("%s: the encrypted canary is readable in %v", when, hits)
		}
	}

	before := map[string][]string{
		"channels": rowDump(ctx, t, raw, "channels"),
		"messages": rowDump(ctx, t, raw, "messages"),
	}
	assertCanaries(t, "before any switch")

	// Both directions, because both were argued and both must be inert:
	// strict→compliance cannot decrypt what exists, and compliance→strict
	// cannot retroactively protect what was stored in the clear.
	for _, mode := range []string{storage.EncryptionModeCompliance, storage.EncryptionModeStrict} {
		t.Run("after switching to "+mode, func(t *testing.T) {
			if _, setErr := store.SetEncryptionMode(ctx, mode); setErr != nil {
				t.Fatalf("SetEncryptionMode(%s): %v", mode, setErr)
			}
			for table, want := range before {
				got := rowDump(ctx, t, raw, table)
				if len(got) != len(want) {
					t.Fatalf("%s holds %d rows after the switch, want %d", table, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Errorf("%s row changed across the mode switch:\nbefore %s\nafter  %s", table, want[i], got[i])
					}
				}
			}
			assertCanaries(t, "after switching to "+mode)
		})
	}
}

// The drill's two readers below walk whichever catalogue the driver keeps —
// information_schema on PostgreSQL, sqlite_master plus the table_info pragma
// on SQLite — because the property they assert is about the STORED BYTES and
// belongs to both drivers. Only the catalogue query and the rendering
// functions differ; the assertions above are the same on either.

// rowDump renders every row of one table as text, ordered by id, so two
// dumps can be compared byte for byte. A whole-row rendering is the point:
// it covers columns this test never had to know about, including any a later
// slice adds.
func rowDump(ctx context.Context, t *testing.T, raw *testdb.Raw, table string) []string {
	t.Helper()

	// table is a literal from this file's own call sites, never input.
	query := `SELECT t::text FROM ` + table + ` t ORDER BY t.id`
	if raw.Driver() == testdb.DriverSQLite {
		// SQLite has no record-to-text cast, so the row is assembled from its
		// own columns. quote() renders NULL, text and blobs unambiguously,
		// and the column list comes from the catalogue for the same reason
		// the PostgreSQL side casts the whole row: a column a later slice
		// adds is covered without anybody adding it here.
		rendered := make([]string, 0, 16)
		for _, name := range tableColumns(ctx, t, raw, table) {
			rendered = append(rendered, fmt.Sprintf("quote(%q)", name))
		}
		query = `SELECT ` + strings.Join(rendered, ` || '|' || `) + ` FROM ` + table + ` ORDER BY id`
	}

	rows := raw.Query(ctx, t, query)
	var out []string
	for rows.Next() {
		var line string
		if scanErr := rows.Scan(&line); scanErr != nil {
			t.Fatalf("dump %s: %v", table, scanErr)
		}
		out = append(out, line)
	}
	if rows.Err() != nil {
		t.Fatalf("dump %s: %v", table, rows.Err())
	}
	return out
}

// tableColumns lists one table's columns in declaration order.
func tableColumns(ctx context.Context, t *testing.T, raw *testdb.Raw, table string) []string {
	t.Helper()

	rows := raw.Query(ctx, t,
		`SELECT p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
		 WHERE m.type = 'table' AND m.name = ? ORDER BY p.cid`, table)

	var names []string
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			t.Fatalf("list columns of %s: %v", table, scanErr)
		}
		names = append(names, name)
	}
	if rows.Err() != nil {
		t.Fatalf("list columns of %s: %v", table, rows.Err())
	}
	if len(names) == 0 {
		t.Fatalf("table %s has no columns; the catalogue query is wrong", table)
	}
	return names
}

// storedColumn is one column the drill has to look inside: a text or a byte
// column somewhere in the schema.
type storedColumn struct {
	table, name string
	isBytes     bool
}

// scanDatabaseFor is the drill's database dump, done in SQL: every text and
// byte column of every table in the schema is searched for needle, and the
// "table.column" of each hit is reported.
//
// It walks the catalogue rather than a list of columns this test knows about,
// so a later slice that stores something readable beside a ciphertext turns
// this red without anybody remembering to add it here.
func scanDatabaseFor(ctx context.Context, t *testing.T, raw *testdb.Raw, needle string) []string {
	t.Helper()

	var hits []string
	for _, c := range storedColumns(ctx, t, raw) {
		var (
			query string
			arg   any = needle
		)
		if raw.Driver() == testdb.DriverSQLite {
			// A blob column needs a blob needle, or SQLite compares a blob
			// against text and never matches.
			if c.isBytes {
				arg = []byte(needle)
			}
			query = fmt.Sprintf(`SELECT count(*) FROM %q WHERE instr(%q, ?) > 0`, c.table, c.name)
		} else {
			// A bytea rendered ::text comes back as \x hex, which would hide
			// the very bytes this looks for; escape output keeps printable
			// ASCII printable, so a canary stored raw in a bytea is still
			// found.
			rendered := fmt.Sprintf("%q::text", c.name)
			if c.isBytes {
				rendered = fmt.Sprintf("encode(%q, 'escape')", c.name)
			}
			query = fmt.Sprintf(`SELECT count(*) FROM %q WHERE position(? in %s) > 0`, c.table, rendered)
		}
		// Identifiers come from the catalogue and are quoted; the needle is
		// the only value, and it is bound.
		var found int
		if scanErr := raw.QueryRow(ctx, query, arg).Scan(&found); scanErr != nil {
			t.Fatalf("scan %s.%s: %v", c.table, c.name, scanErr)
		}
		if found > 0 {
			hits = append(hits, c.table+"."+c.name)
		}
	}
	sort.Strings(hits)
	return hits
}

// storedColumns is the catalogue walk, in whichever dialect this driver keeps
// its catalogue.
func storedColumns(ctx context.Context, t *testing.T, raw *testdb.Raw) []storedColumn {
	t.Helper()

	query := `SELECT table_name, column_name, data_type FROM information_schema.columns
	          WHERE table_schema = 'public'
	            AND data_type IN ('text', 'character varying', 'character', 'bytea')
	          ORDER BY table_name, column_name`
	byteType := "bytea"
	if raw.Driver() == testdb.DriverSQLite {
		query = `SELECT m.name, p.name, lower(p.type)
		         FROM sqlite_master m JOIN pragma_table_info(m.name) p
		         WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'
		           AND lower(p.type) IN ('text', 'blob')
		         ORDER BY m.name, p.name`
		byteType = "blob"
	}

	rows := raw.Query(ctx, t, query)
	var columns []storedColumn
	for rows.Next() {
		var table, name, dataType string
		if scanErr := rows.Scan(&table, &name, &dataType); scanErr != nil {
			t.Fatalf("list columns: %v", scanErr)
		}
		columns = append(columns, storedColumn{table: table, name: name, isBytes: dataType == byteType})
	}
	if rows.Err() != nil {
		t.Fatalf("list columns: %v", rows.Err())
	}
	if len(columns) == 0 {
		t.Fatal("the catalogue reported no text columns at all; this drill proves nothing")
	}
	return columns
}
