package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/calls"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/sqlitestore"
)

// envHomeAddr moves home mode off loopback. It is deliberately an explicit
// setting and deliberately fails closed: the session cookies are Secure, and
// a browser sends a Secure cookie over plain HTTP only to localhost. A home
// instance published to a LAN over http:// therefore does not silently
// degrade to an insecure session — it visibly does not sign in until the
// operator puts a reverse proxy with TLS in front of it, which is the
// supported shape (ADR 012, docs/hardening.md).
const envHomeAddr = "HAMLANEH_HOME_ADDR"

const (
	// defaultHomeAddr is loopback, because home mode is one machine and
	// there is no TLS terminator in this deployment to make anything else
	// safe.
	defaultHomeAddr = "127.0.0.1:8080"

	// homeDirName is what home mode calls its directory under the per-user
	// application directory os.UserConfigDir reports: %AppData% on Windows,
	// ~/Library/Application Support on macOS, $XDG_CONFIG_HOME or ~/.config
	// on Linux. One directory holds the database, the uploaded bytes and the
	// keys, which is what makes it the honest backup unit (ADR 012).
	homeDirName = "hamlaneh"

	// homeDBFile is the SQLite database inside that directory. WAL mode adds
	// two siblings (-wal, -shm); all three belong to a backup.
	homeDBFile = "hamlaneh.db"

	// The two keys server mode gets from deploy/install.sh through the
	// environment. Home mode has no installer, so they live beside the
	// database they protect.
	fileKeyFile  = "file-url.key"
	auditKeyFile = "audit.key"

	// homeKeyBytes is the entropy behind a generated key. Both consumers MAC
	// with SHA-256, whose block-sized key is 32 bytes.
	homeKeyBytes = 32
)

// Permissions for what home mode creates. A household's chat, its files and
// its keys are one person's data on a shared machine; nothing else on it has
// any business reading them. Windows has no POSIX mode bits and ignores
// these — there the user profile's own ACL is what keeps other accounts out.
const (
	dataDirPerm fs.FileMode = 0o700
	keyFilePerm fs.FileMode = 0o600
)

// mode is everything the two deployment modes differ by, and the list is
// deliberately this short: home mode is the same server with a second storage
// driver, a loopback bind and no media plane. Every other decision — the
// security headers, the cookies, the authorization, the bootstrap admin — is
// one implementation serving both, which is what stops home mode from
// becoming a weaker product that happens to share a name.
type mode struct {
	// home selects the SQLite driver and the behaviour around it.
	home bool

	// addr is the listen address.
	addr string

	// dataDir holds the uploaded bytes in both modes, plus home mode's
	// database and keys.
	dataDir string

	// compress gzips the embedded web build on the way out.
	compress bool

	// publicURL is the instance's own origin: the WebSocket handshake's
	// Origin check, the invitation links and the reset links are built from
	// it.
	publicURL string
}

// serverMode is the deployment the compose stack runs: PostgreSQL from the
// libpq environment, every interface bound, Caddy in front doing TLS and
// compression.
func serverMode() mode {
	dir := os.Getenv(blobstore.EnvDataDir)
	if dir == "" {
		dir = blobstore.DefaultDataDir
	}
	return mode{
		addr:      listenAddr,
		dataDir:   dir,
		compress:  os.Getenv(httpserver.EnvCompressResponses) == "1",
		publicURL: os.Getenv(passwordreset.EnvPublicURL),
	}
}

// homeMode is the single-binary deployment: one SQLite file, loopback, no
// media server, and compression on because nothing is in front of this
// process to do it.
//
// It reads configuration only. Nothing here creates a directory or touches
// the database, so deciding where home mode WOULD live is free.
func homeMode() (mode, error) {
	// Home mode has no SFU, and ADR 012 closed ADR 005's deferred question
	// by refusing to grow one: LiveKit is a second process a single binary
	// cannot absorb, and an SFU exists to traverse hostile NATs between
	// strangers, which is the opposite of one household's machine. The UI
	// already omits call controls when the instance document says calls are
	// off, so nothing here breaks a button. A LiveKit environment does mean
	// the operator expects something this mode cannot do, and quietly
	// ignoring their configuration is how an install ends up lying about
	// itself — so it stops startup, the same way a half-set one does in
	// either mode.
	for _, name := range []string{calls.EnvAPIKey, calls.EnvAPISecret, calls.EnvURL} {
		if os.Getenv(name) != "" {
			return mode{}, fmt.Errorf(
				"home mode ships without calls, so %s must not be set: "+
					"a media server is a second process a single binary does not run (ADR 012)", name)
		}
	}

	dir := os.Getenv(blobstore.EnvDataDir)
	if dir == "" {
		cfgDir, err := os.UserConfigDir()
		if err != nil {
			return mode{}, fmt.Errorf(
				"home mode: no per-user application directory on this system, so set %s: %w",
				blobstore.EnvDataDir, err)
		}
		dir = filepath.Join(cfgDir, homeDirName)
	}

	addr := envOr(envHomeAddr, defaultHomeAddr)
	return mode{
		home:    true,
		addr:    addr,
		dataDir: dir,
		// On unless the install says otherwise, which is the inverse of
		// server mode: there Caddy already runs `encode zstd gzip` and this
		// process should not spend the CPU, here the ~560 KB web bundle
		// would otherwise cross the household's link uncompressed
		// (internal/httpserver/compress.go).
		compress: os.Getenv(httpserver.EnvCompressResponses) != "0",
		// The origin a browser on this machine actually sends. Without one
		// the WebSocket gateway refuses every upgrade, so leaving it empty
		// would ship a chat that cannot receive a message.
		publicURL: envOr(passwordreset.EnvPublicURL, "http://"+addr),
	}, nil
}

// dbPath is where home mode's database lives. Server mode has none.
func (m mode) dbPath() string { return filepath.Join(m.dataDir, homeDBFile) }

// prepare creates home mode's data directory, so the keys, the database and
// the blob store all find it there rather than each discovering separately
// that the path is unusable. Server mode's directory is blobstore's own job,
// exactly as before.
func (m mode) prepare() error {
	if !m.home {
		return nil
	}
	// gosec flags a directory built from outside configuration. It is
	// HAMLANEH_DATA_DIR or this OS's per-user application directory — the
	// operator's own choice, as trusted as the DSN in server mode, and
	// nothing a user sends ever names a path here (see internal/blobstore).
	if err := os.MkdirAll(m.dataDir, dataDirPerm); err != nil { // #nosec G703 -- operator configuration, not user input
		return fmt.Errorf("create the home data directory %s: %w", m.dataDir, err)
	}
	return nil
}

// keys returns the two secrets that must exist before anything else does: the
// file-URL signing key every read of an uploaded file is checked against, and
// the audit chain key that makes the log evidence rather than a table of rows.
//
// Both are answerable with no network at all, which is why they are the first
// thing startup asks for in either mode. Server mode requires them in the
// environment, where deploy/install.sh generates them. Home mode has no
// installer and no .env file, so it keeps them in the data directory — the
// environment still wins where it is set, and either form is the same base64
// text, so one can be pasted where the other is expected.
func (m mode) keys() (*filesign.Signer, *audit.Chain, error) {
	if !m.home {
		signer, err := filesign.FromEnv()
		if err != nil {
			return nil, nil, err
		}
		chain, err := audit.FromEnv()
		if err != nil {
			return nil, nil, err
		}
		return signer, chain, nil
	}

	fileKey, err := homeKey(m.dataDir, filesign.EnvKey, fileKeyFile)
	if err != nil {
		return nil, nil, err
	}
	signer, err := filesign.New(fileKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Join(m.dataDir, fileKeyFile), err)
	}

	auditKey, err := homeKey(m.dataDir, audit.EnvKey, auditKeyFile)
	if err != nil {
		return nil, nil, err
	}
	chain, err := audit.New(auditKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Join(m.dataDir, auditKeyFile), err)
	}
	return signer, chain, nil
}

// openStore opens the driver this mode runs on. There is no fallback in
// either direction: a server-mode instance whose PostgreSQL is unreachable
// stops here rather than quietly opening an empty SQLite file beside itself
// and forking the organization's data (ADR 012).
func (m mode) openStore(ctx context.Context) (store, error) {
	if m.home {
		return openHomeStore(ctx, m.dbPath())
	}
	return openStorage(ctx)
}

// downgradeHint is appended to every failure to open the home database,
// because one cause is worth naming and an operator cannot be expected to
// read golang-migrate's spelling of it ("no migration found for version 20").
//
// It is phrased as a condition rather than a diagnosis: the same open also
// fails for a file this process cannot read, and claiming a downgrade there
// would send somebody looking for the wrong problem.
const downgradeHint = "\n(if this database was written by a NEWER version of hamlaneh-server, " +
	"an older binary cannot open it: install that version again, or move the file aside to start fresh)"

// openHomeStore opens the home database, creating it and applying the SQLite
// migration tree on first run, and refuses one this binary is too old for.
//
// That refusal is the point, and it has two layers because the failure it
// prevents is somebody's only copy of their household's history. golang-
// migrate refuses outright to run against a schema version its source does
// not contain, which is what a newer release leaves behind; and Open has run
// every migration this binary carries by the time it returns, so a version
// that STILL does not match can only be a schema this code does not agree
// with. Either way the answer is to stop: there is no down path here and a
// process that wrote rows through the disagreement would be the corruption.
func openHomeStore(ctx context.Context, path string) (*sqlitestore.Store, error) {
	slog.Info("opening the home database", "path", path)
	st, err := sqlitestore.Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open the home database at %s: %w%s", path, err, downgradeHint)
	}
	if readyErr := st.Ready(ctx); readyErr != nil {
		st.Close()
		return nil, fmt.Errorf("the home database at %s does not match this binary: %w%s",
			path, readyErr, downgradeHint)
	}
	slog.Info("home database ready")
	return st, nil
}

// homeKey returns the key this install keeps in file inside dir, generating
// one on first run. env wins where it is set, so an operator who already has
// the key keeps it and nothing is written.
//
// The value is 32 bytes of crypto/rand, base64 — the same shape the
// environment carries.
func homeKey(dir, env, file string) ([]byte, error) {
	if v := os.Getenv(env); v != "" {
		return []byte(v), nil
	}
	path := filepath.Join(dir, file)

	key, found, err := readKey(path)
	if err != nil {
		return nil, err
	}
	if found {
		return key, nil
	}

	buf := make([]byte, homeKeyBytes)
	if _, randErr := rand.Read(buf); randErr != nil {
		return nil, fmt.Errorf("generate a key for %s: %w", path, randErr)
	}
	minted := []byte(base64.StdEncoding.EncodeToString(buf))

	// O_EXCL, not a plain write: two starts at the same moment must not each
	// mint a key, because the loser's signature would then verify against
	// nothing anybody kept. Whoever creates the file wins and the other
	// reads it.
	// #nosec G304 -- a fixed file name under the data directory the operator configured
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFilePerm)
	if errors.Is(err, fs.ErrExist) {
		key, found, err = readKey(path)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("%s disappeared while it was being created", path)
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err = f.Write(minted); err != nil {
		return nil, errors.Join(fmt.Errorf("write %s: %w", path, err), f.Close())
	}
	if err = f.Close(); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	slog.Info("generated a key for this install", "path", path)
	return minted, nil
}

// readKey reads a stored key, reporting found=false when there is no file
// there yet. The value is trimmed, so a key an operator edited in with a text
// editor is not a different key because of a trailing newline.
func readKey(path string) (key []byte, found bool, err error) {
	// #nosec G304 -- a fixed file name under the data directory the operator configured
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		return bytes.TrimSpace(data), true, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
}

// envOr is the environment variable, or fallback when it is unset or empty.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
