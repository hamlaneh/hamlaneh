package main

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/bootstrap"
	"github.com/hamlaneh/hamlaneh/server/internal/calls"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/mailer"
	"github.com/hamlaneh/hamlaneh/server/internal/oidc"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/sqlitestore"
)

// homeTestEnv gives a test its own data directory and clears every variable
// the boot path reads. A developer's shell must not be able to change what
// these tests assert — and one of them (a stray SMTP or LiveKit variable)
// would otherwise stop the process instead of failing an assertion.
func homeTestEnv(t *testing.T) string {
	t.Helper()
	for _, name := range []string{
		blobstore.EnvDataDir, envHomeAddr,
		filesign.EnvKey, audit.EnvKey,
		calls.EnvAPIKey, calls.EnvAPISecret, calls.EnvURL,
		passwordreset.EnvPublicURL, httpserver.EnvCompressResponses,
		bootstrap.EnvUsername, bootstrap.EnvPassword, bootstrap.EnvLocale,
		mailer.EnvHost, mailer.EnvPort, mailer.EnvUsername, mailer.EnvPassword,
		mailer.EnvFrom, mailer.EnvFromName, mailer.EnvEncryption,
		oidc.EnvIssuer, oidc.EnvClientID, oidc.EnvClientSecret, oidc.EnvProviderName,
	} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	t.Setenv(blobstore.EnvDataDir, dir)
	return dir
}

// TestHomeModeDefaults pins what home mode IS, which is four decisions from
// ADR 012: loopback, its own data directory, compression on (nothing is in
// front of this process), and an origin the browser on this machine will
// actually send.
func TestHomeModeDefaults(t *testing.T) {
	dir := homeTestEnv(t)

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if !m.home {
		t.Error("homeMode().home = false, want true")
	}
	if m.addr != defaultHomeAddr {
		t.Errorf("addr = %q, want %q", m.addr, defaultHomeAddr)
	}
	if m.dataDir != dir {
		t.Errorf("dataDir = %q, want %q", m.dataDir, dir)
	}
	if !m.compress {
		t.Error("compress = false, want true: home mode has no proxy to compress for it")
	}
	if want := "http://" + defaultHomeAddr; m.publicURL != want {
		t.Errorf("publicURL = %q, want %q", m.publicURL, want)
	}
	if got, want := m.dbPath(), filepath.Join(dir, homeDBFile); got != want {
		t.Errorf("dbPath() = %q, want %q", got, want)
	}
}

// TestHomeModeOverrides covers the three knobs an operator has, including the
// LAN bind ADR 012 makes explicit.
func TestHomeModeOverrides(t *testing.T) {
	dir := homeTestEnv(t)
	t.Setenv(envHomeAddr, "0.0.0.0:9000")
	t.Setenv(passwordreset.EnvPublicURL, "https://nest.example")
	t.Setenv(httpserver.EnvCompressResponses, "0")

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if m.addr != "0.0.0.0:9000" {
		t.Errorf("addr = %q, want the configured bind", m.addr)
	}
	if m.publicURL != "https://nest.example" {
		t.Errorf("publicURL = %q, want the configured origin", m.publicURL)
	}
	if m.compress {
		t.Error("compress = true, want false when the install turned it off")
	}
	if m.dataDir != dir {
		t.Errorf("dataDir = %q, want %q", m.dataDir, dir)
	}
}

// TestHomeDataDirDefaultsToUserConfigDir is gate clause 4's portability half:
// with nothing configured the database lands in the per-user application
// directory of whatever OS this is — %AppData% on Windows, ~/Library/
// Application Support on macOS, $XDG_CONFIG_HOME or ~/.config on Linux.
func TestHomeDataDirDefaultsToUserConfigDir(t *testing.T) {
	homeTestEnv(t)
	t.Setenv(blobstore.EnvDataDir, "")

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("this OS has no per-user configuration directory: %v", err)
	}
	if want := filepath.Join(cfgDir, homeDirName); m.dataDir != want {
		t.Errorf("dataDir = %q, want %q", m.dataDir, want)
	}
	if _, err := os.Stat(m.dataDir); err == nil {
		t.Errorf("homeMode() created %s; deciding where the directory is must not make one", m.dataDir)
	}
}

// TestHomeModeRefusesLiveKit closes ADR 005's deferred item the way ADR 012
// decided it: home mode has no SFU, so a LiveKit environment here is a
// misconfiguration to name, never a media plane to half-start.
func TestHomeModeRefusesLiveKit(t *testing.T) {
	for _, name := range []string{calls.EnvAPIKey, calls.EnvAPISecret, calls.EnvURL} {
		t.Run(name, func(t *testing.T) {
			homeTestEnv(t)
			t.Setenv(name, "set")

			_, err := homeMode()
			if err == nil {
				t.Fatalf("homeMode() with %s set returned no error, want a refusal", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		})
	}
}

// TestServerModeUnchanged pins the other half of "server mode's behaviour must
// not change": the bind, the data directory default and compression-off are
// what they were before home mode existed.
func TestServerModeUnchanged(t *testing.T) {
	homeTestEnv(t)
	t.Setenv(blobstore.EnvDataDir, "")

	m := serverMode()
	if m.home {
		t.Error("serverMode().home = true, want false")
	}
	if m.addr != listenAddr {
		t.Errorf("addr = %q, want %q", m.addr, listenAddr)
	}
	if m.dataDir != blobstore.DefaultDataDir {
		t.Errorf("dataDir = %q, want %q", m.dataDir, blobstore.DefaultDataDir)
	}
	if m.compress {
		t.Error("compress = true, want false: the compose stack's Caddy compresses")
	}

	t.Setenv(httpserver.EnvCompressResponses, "1")
	if !serverMode().compress {
		t.Error("compress = false with the variable set to 1, want true")
	}
}

// TestServerModeNeverOpensSQLite is ADR 012's "misconfiguration fails, never
// falls back": a server-mode instance with no database configured must stop,
// not quietly open an empty SQLite file beside itself and fork the
// organization's data.
func TestServerModeNeverOpensSQLite(t *testing.T) {
	dir := homeTestEnv(t)
	for _, name := range []string{"PGHOST", "PGDATABASE", "PGUSER", "PGPASSWORD"} {
		t.Setenv(name, "")
	}

	m := serverMode()
	m.dataDir = dir
	if _, err := m.openStore(context.Background()); err == nil {
		t.Fatal("server mode opened a store with no PostgreSQL configured, want an error")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the data directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("server mode wrote %v into the data directory; it must never create a database file", entries)
	}
}

// TestHomeModeOpensTheSQLiteDriver is the assertion that nothing else can
// make: the store home mode hands the HTTP surface is the SQLite one.
func TestHomeModeOpensTheSQLiteDriver(t *testing.T) {
	homeTestEnv(t)
	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if prepErr := m.prepare(); prepErr != nil {
		t.Fatalf("prepare: %v", prepErr)
	}

	st, err := m.openStore(context.Background())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer st.Close()

	if _, ok := st.(*sqlitestore.Store); !ok {
		t.Errorf("home mode opened %T, want *sqlitestore.Store", st)
	}
	if _, err := os.Stat(m.dbPath()); err != nil {
		t.Errorf("first run did not create %s: %v", m.dbPath(), err)
	}
}

// TestHomeModeBootsAndReusesItsDatabase is the whole boot path, twice, with no
// PostgreSQL anywhere: the process serves /readyz — which pings the database
// and compares its schema version — and a second start on the same data
// directory finds the first run's account rather than a fresh or broken file.
func TestHomeModeBootsAndReusesItsDatabase(t *testing.T) {
	dir := homeTestEnv(t)
	addr := freeAddr(t)
	t.Setenv(envHomeAddr, addr)
	t.Setenv(bootstrap.EnvUsername, "amir")
	t.Setenv(bootstrap.EnvPassword, "a password long enough to pass validation")

	stop := runHome(t, addr)
	for _, name := range []string{homeDBFile, fileKeyFile, auditKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("first run did not create %s: %v", name, err)
		}
	}
	stop()

	// No bootstrap variables the second time: the account must come from the
	// database, and a second start must not fail on the file the first left.
	t.Setenv(bootstrap.EnvUsername, "")
	t.Setenv(bootstrap.EnvPassword, "")
	runHome(t, addr)()

	st, err := openHomeStore(context.Background(), filepath.Join(dir, homeDBFile))
	if err != nil {
		t.Fatalf("reopen the home database: %v", err)
	}
	defer st.Close()

	count, err := st.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("users after two starts = %d, want 1 (the first run's admin, kept)", count)
	}
}

// TestHomeModeRefusesADatabaseFromANewerBinary is the failure that matters
// here. An older binary opening a database a newer one has already migrated
// must stop: golang-migrate's Up is a no-op against a schema it does not know
// about, so without this the process would run happily and write rows through
// code that disagrees with the schema on disk.
func TestHomeModeRefusesADatabaseFromANewerBinary(t *testing.T) {
	dir := homeTestEnv(t)
	path := filepath.Join(dir, homeDBFile)
	ctx := context.Background()

	st, err := openHomeStore(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	st.Close()

	// What a newer binary's migration tree would have left behind.
	bumpSchemaVersion(t, path)

	reopened, err := openHomeStore(ctx, path)
	if err == nil {
		reopened.Close()
		t.Fatal("opened a database a newer binary had migrated, want a refusal")
	}
	if !strings.Contains(err.Error(), downgradeHint) {
		t.Errorf("error %q does not tell the operator a newer version may have written this database", err)
	}

	// And the refusal is non-destructive: putting the newer binary back —
	// which is what the message tells the operator to do — finds the database
	// exactly as it was.
	restoreSchemaVersion(t, path)
	restored, err := openHomeStore(ctx, path)
	if err != nil {
		t.Fatalf("the refused open damaged the database: %v", err)
	}
	restored.Close()
}

// TestHomeKeysArePersistedAndReused is why home mode keeps its keys in the
// data directory rather than minting them per boot: a file URL signed before
// a restart has to still verify after it, and every audit entry written
// before a restart has to still verify after it.
func TestHomeKeysArePersistedAndReused(t *testing.T) {
	dir := homeTestEnv(t)
	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if prepErr := m.prepare(); prepErr != nil {
		t.Fatalf("prepare: %v", prepErr)
	}

	first, _, err := m.keys()
	if err != nil {
		t.Fatalf("first keys(): %v", err)
	}
	second, _, err := m.keys()
	if err != nil {
		t.Fatalf("second keys(): %v", err)
	}

	id, at := uuid.New(), time.Unix(1_700_000_000, 0)
	if a, b := first.FileURLAt(id, blobstore.Original, at), second.FileURLAt(id, blobstore.Original, at); a != b {
		t.Errorf("a restart re-signed the same file differently:\n %s\n %s", a, b)
	}

	// 0600 on the two files, so a chat's keys are not readable by other
	// accounts on the machine. Windows has no POSIX mode bits, and Go
	// reports 0666 there whatever was requested; the check is skipped rather
	// than asserted falsely.
	for _, name := range []string{fileKeyFile, auditKeyFile} {
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			t.Fatalf("stat %s: %v", name, statErr)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 && runtimeHasPOSIXPerms() {
			t.Errorf("%s mode = %o, want no group or world access", name, perm)
		}
	}
}

// TestHomeKeysPreferTheEnvironment keeps the installer's path working in home
// mode: an operator who already has the two keys keeps using them, and
// nothing new is written beside the database.
func TestHomeKeysPreferTheEnvironment(t *testing.T) {
	dir := homeTestEnv(t)
	const supplied = "an externally supplied key of sufficient length"
	t.Setenv(filesign.EnvKey, supplied)
	t.Setenv(audit.EnvKey, supplied)

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if prepErr := m.prepare(); prepErr != nil {
		t.Fatalf("prepare: %v", prepErr)
	}
	if _, _, err := m.keys(); err != nil {
		t.Fatalf("keys(): %v", err)
	}

	for _, name := range []string{fileKeyFile, auditKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("wrote %s although the environment supplied the key (%v)", name, err)
		}
	}
}

// runHome starts home mode and waits until it answers /readyz. The returned
// function shuts it down and fails the test if the run itself failed; it is
// safe to call once.
func runHome(t *testing.T, addr string) func() {
	t.Helper()
	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- start(ctx, m) }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("home mode stopped before it was ready: %v", err)
		default:
		}
		if ready(t, addr) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("home mode did not answer /readyz within 30s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("home mode run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("home mode did not shut down within 30s")
		}
	}
}

// ready reports whether the readiness probe — a database ping plus a schema
// version comparison — passes. It is what makes this a test of the SQLite
// database behind the server rather than of the listener.
func ready(t *testing.T, addr string) bool {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/readyz") //nolint:gosec // the address is this test's own listener
	if err != nil {
		return false
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close the probe response: %v", err)
		}
	}()
	return resp.StatusCode == http.StatusOK
}

// freeAddr reserves a loopback port and releases it, so the server under test
// can bind something no other test is using.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return addr
}

// bumpSchemaVersion rewrites the migration bookkeeping to a version this
// binary's migration tree does not contain — what an install would look like
// after a newer release ran and the operator went back to an older one.
func bumpSchemaVersion(t *testing.T, path string) {
	t.Helper()
	execDirect(t, path, "UPDATE schema_migrations SET version = version + 1")
}

// restoreSchemaVersion undoes it, standing in for the operator reinstalling
// the version that wrote the database.
func restoreSchemaVersion(t *testing.T, path string) {
	t.Helper()
	execDirect(t, path, "UPDATE schema_migrations SET version = version - 1")
}

// execDirect runs one statement against the home database through a handle of
// this test's own, with the driver's pragmas so the file is treated exactly as
// the driver treats it.
func execDirect(t *testing.T, path, stmt string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqlitestore.DSN(path))
	if err != nil {
		t.Fatalf("open the database directly: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close the direct handle: %v", err)
		}
	}()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// runtimeHasPOSIXPerms reports whether this OS reports the permission bits a
// file was created with. Windows does not.
func runtimeHasPOSIXPerms() bool { return os.PathSeparator == '/' }
