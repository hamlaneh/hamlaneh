package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/bootstrap"
	"github.com/hamlaneh/hamlaneh/server/internal/calls"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/sqlitestore"
	"github.com/hamlaneh/hamlaneh/server/internal/wsgateway"
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
	// with SHA-256, whose 32-byte OUTPUT is the security level worth matching;
	// past that a longer key buys nothing. (SHA-256's BLOCK is 64 bytes — a
	// different number, and not the one that matters here: HMAC pads or hashes
	// the key to the block either way.)
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

// gatewayOptions is what the realtime gateway is configured with beyond the
// public origin, and the list differs by mode in exactly one entry.
//
// Home mode binds 127.0.0.1 and prints that address, but a person types
// localhost — the same machine, the same port, the same trust boundary. The
// handshake's Origin check is a single exact origin, so without this the page
// loads under the other spelling and every WebSocket upgrade is refused: a
// chat that appears to work and silently receives nothing.
//
// Server mode gets no such allowance and must not: there, one origin is the
// CSRF defense a browser cannot otherwise provide on a handshake. It is a
// method rather than an `if` at the call site so that the difference is
// something a test can hold — see TestServerModeGetsNoOriginAllowance.
func (m mode) gatewayOptions() []wsgateway.Option {
	if !m.home {
		return nil
	}
	return []wsgateway.Option{wsgateway.WithLoopbackAlias()}
}

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
	warnIfUnprotectedOnWindows(m.dataDir)
	return nil
}

// warnIfUnprotectedOnWindows says so when the data directory is somewhere
// Windows will not protect it.
//
// The 0700 and 0600 above are POSIX mode bits, and Go maps them on Windows to
// nothing but the read-only attribute: no DACL is written, so the protection
// is entirely INHERITED from the parent. Under the default %AppData%\hamlaneh
// that inherits the user profile's ACL and the intended protection holds. A
// HAMLANEH_DATA_DIR pointing at a volume root — C:\hamlaneh, or a data drive —
// inherits the volume-root ACL instead, where BUILTIN\Users get read access
// and Authenticated Users get modify. Every local account can then read
// audit.key and file-url.key, and nothing anywhere says so.
//
// This warns rather than refuses: it is the operator's own machine and their
// own choice of directory, and a hard failure on a valid single-user setup
// would be worse than a line they can act on. It also deliberately does not
// try to read the actual ACL — that is a Windows API call and a pile of
// platform-specific code to answer a question a location check answers well
// enough. A false warning costs a sentence; a missed one costs the keys.
func warnIfUnprotectedOnWindows(dataDir string) {
	if runtime.GOOS != "windows" {
		return
	}
	profile := os.Getenv("USERPROFILE")
	if profile != "" && underDir(dataDir, profile) {
		return
	}
	slog.Warn("the home data directory is outside your Windows user profile, "+
		"so it does not inherit the profile's access rules and other accounts on this machine "+
		"may be able to read the database and the keys beside it; "+
		"move it under %AppData% or restrict it yourself (icacls)",
		"data_dir", dataDir, "user_profile", profile)
}

// underDir reports whether path is dir or something inside it. Both are
// resolved to absolute, cleaned form first.
//
// Case is ignored on Windows, where the file system ignores it, and honoured
// everywhere else, where /home/Amir and /home/amir are two directories. Only
// the Windows answer has a caller today; getting the other one right anyway is
// cheaper than leaving a function that lies on the platform CI runs.
func underDir(path, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absPath, absDir = filepath.Clean(absPath), filepath.Clean(absDir)
	if runtime.GOOS == "windows" {
		absPath, absDir = strings.ToLower(absPath), strings.ToLower(absDir)
	}
	if absPath == absDir {
		return true
	}
	// The separator matters: C:\Users\amirtwo must not read as inside
	// C:\Users\amir.
	return strings.HasPrefix(absPath, absDir+string(os.PathSeparator))
}

// The first administrator of a home install.
const (
	// homeAdminUsername is the account name when the operator named none.
	// The password beside it is never a constant — see mintPassword.
	homeAdminUsername = "admin"

	// homeAdminPasswordBytes is the entropy behind a generated password.
	// Fifteen bytes is 120 bits and encodes to exactly 24 base32 characters
	// with no padding, which is what makes it a thing somebody can read off
	// a console and type without a rule about trailing "=" signs.
	homeAdminPasswordBytes = 15
)

// firstAdmin is how a fresh instance gets its one administrator.
//
// Server mode gets the pair from deploy/install.sh through the environment.
// Home mode has no installer, and before this it had no answer at all: a
// household user who downloaded the binary got a warning telling them to set
// two variables and could not sign in. So home mode generates the password
// itself when nobody supplied one.
//
// The alternative shape — a first-run setup screen reachable only from
// loopback — was refused for one reason that outweighs its nicer UX: it is a
// privileged, unauthenticated HTTP path that must be proven shut afterwards
// and proven unreachable from a LAN bind, whereas a console announcement has
// no network surface to close. "Impossible rather than unlikely" is the
// requirement, and no endpoint is the only way to be sure there is no endpoint.
// (It is also what ADR 012 step 6 already specified: reuse the existing
// empty-users-table path, no contract change, no new endpoint.)
type firstAdmin struct {
	cfg bootstrap.AdminConfig
	// present is bootstrap's "is there a configuration to act on at all".
	present bool
	// minted is true when the password in cfg was generated by this process
	// and therefore exists nowhere else yet. It is the condition the console
	// announcement is made under, and only ever together with bootstrap
	// reporting that it actually created the account.
	minted bool
}

// firstAdmin reads the bootstrap configuration for this mode, generating what
// home mode is missing.
//
// Nothing here writes anything or looks at the database: whether an admin is
// actually created stays bootstrap.EnsureAdmin's decision, gated on an empty
// users table. A generated password on a second start is therefore computed
// and thrown away, which is exactly what makes the restart case safe rather
// than dependent on this function knowing what happened last time.
func (m mode) firstAdmin() (firstAdmin, error) {
	cfg, present := bootstrap.AdminFromEnv()
	if present || !m.home {
		return firstAdmin{cfg: cfg, present: present}, nil
	}

	// Half a pair is not an error here the way it is for SMTP or LiveKit,
	// because neither half is dangerous alone: a name with no password gets
	// a generated one, and a password with no name gets the default name.
	if cfg.Username == "" {
		cfg.Username = homeAdminUsername
	}
	if cfg.Password != "" {
		return firstAdmin{cfg: cfg, present: true}, nil
	}

	pw, err := mintPassword()
	if err != nil {
		return firstAdmin{}, err
	}
	cfg.Password = pw
	return firstAdmin{cfg: cfg, present: true, minted: true}, nil
}

// mintPassword returns a fresh password for the first administrator.
//
// crypto/rand, never a literal and never derived from anything on the machine:
// "no default credentials, ever" is a launch-blocking rule, and a password an
// attacker could compute from the hostname or the install time would be one.
//
// base32 rather than base64 because this is read off a console and typed by
// hand: its alphabet is A-Z and 2-7, so there is no 0/O and no 1/l/I to
// misread, and no case to get wrong.
func mintPassword() (string, error) {
	buf := make([]byte, homeAdminPasswordBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a password for the first administrator: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// announceFirstAdmin shows a generated password to whoever started the
// process, once, on the console.
//
// It goes to stdout and not through slog on purpose. A log record is the thing
// that gets collected, rotated, shipped and grepped later; this is a credential
// with a lifetime of one login, and the console the operator is currently
// looking at is the whole of where it should exist. Nothing writes it to disk
// either — the account it opens is already flagged must-change-password, so its
// value after that first sign-in is zero.
//
// The consequence is deliberate and worth stating: an operator who loses this
// output before signing in has no way back in, and recovers by moving the data
// directory aside and starting again. That is the honest cost of not keeping a
// live credential somewhere it could be read twice.
func (m mode) announceFirstAdmin(cfg bootstrap.AdminConfig) {
	fmt.Fprintf(stdout, `
==============================================================
 Hamlaneh created the first administrator account.
 This password is shown once and is not stored anywhere.

   username: %s
   password: %s

 Open %s and sign in.
 You will be asked to choose a new password immediately.
==============================================================

`, cfg.Username, cfg.Password, m.publicURL)
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

	fileKey, fileSource, err := homeKey(m.dataDir, filesign.EnvKey, fileKeyFile)
	if err != nil {
		return nil, nil, err
	}
	signer, err := filesign.New(fileKey)
	if err != nil {
		// The source, not the file: an environment-supplied key that is too
		// short must not be reported against a path that does not exist.
		return nil, nil, fmt.Errorf("%s: %w", fileSource, err)
	}

	auditKey, auditSource, err := homeKey(m.dataDir, audit.EnvKey, auditKeyFile)
	if err != nil {
		return nil, nil, err
	}
	chain, err := audit.New(auditKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w%s", auditSource, err, auditKeyRepairHint)
	}
	noteHomeAuditKeyScope()
	return signer, chain, nil
}

// auditKeyRepairHint is appended to a rejected audit key, because the obvious
// reaction to "this key is too short" is to delete the file and let the server
// mint a new one — and that silently orphans the whole audit log. A new key
// verifies nothing written under the old one, and every existing entry then
// reports as broken forever. Restoring the file is nearly always the right
// move, and an operator who has no copy should know what the alternative costs
// BEFORE they choose it rather than after.
const auditKeyRepairHint = "\n(restore this key from a backup if you have one: deleting it and letting a new one " +
	"be generated permanently orphans every audit entry written so far, which will then all fail verification)"

// noteHomeAuditKeyScope states, once, what the audit chain is and is not worth
// in home mode.
//
// internal/audit's package comment rests the guarantee on the key being "not
// in the database to be stolen along with the rows". That holds in server
// mode, where the key is an environment variable of a container and the rows
// are in PostgreSQL. It very nearly evaporates here: audit.key sits in the same
// directory, with the same owner, as hamlaneh.db, so anybody who can rewrite
// the database can also read the key and re-seal the chain invisibly.
//
// What survives is still worth having and is what this says: the chain catches
// tampering that touched the database WITHOUT the key — an edited backup, a
// restore of the wrong file, a tool that wrote rows directly, ordinary
// corruption. It does not catch an attacker who already owns the account this
// process runs as. CLAUDE.md principle 4 is why that is written down rather
// than left for somebody to assume the stronger claim.
func noteHomeAuditKeyScope() {
	slog.Info("home mode keeps the audit key beside the database it protects; " +
		"the chain therefore shows tampering that did not have this machine's user account " +
		"(an edited backup, a direct write, corruption), not tampering by someone who did")
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
// source names where the returned key actually came from — the environment
// variable or the file — so a caller that rejects it can send the operator to
// the right place. Naming the file for a key the environment supplied sends
// somebody hunting a file that does not exist.
//
// The value is 32 bytes of crypto/rand, base64 — the same shape the
// environment carries.
//
// A key file is trusted once it exists and is long enough: a file truncated or
// hand-edited to any 32+ bytes is taken as authoritative, because there is no
// way to tell an operator's deliberate replacement from a damaged file. That
// is why the callers' rejection message has to say what deleting it costs.
func homeKey(dir, env, file string) (key []byte, source string, err error) {
	if v := os.Getenv(env); v != "" {
		return []byte(v), env, nil
	}
	path := filepath.Join(dir, file)

	key, found, err := readKey(path)
	if err != nil {
		return nil, path, err
	}
	if found {
		return key, path, nil
	}

	buf := make([]byte, homeKeyBytes)
	if _, randErr := rand.Read(buf); randErr != nil {
		return nil, path, fmt.Errorf("generate a key for %s: %w", path, randErr)
	}
	minted := []byte(base64.StdEncoding.EncodeToString(buf))

	// O_EXCL, not a plain write: two starts at the same moment must not each
	// mint a key, because the loser's signature would then verify against
	// nothing anybody kept. Whoever creates the file wins and the other
	// reads it.
	//
	// ponytail: a crash or power cut between this create and the write below
	// leaves an empty file, which then fails the length check on every start
	// until a human removes it. The window is microseconds on a household
	// machine and the failure is loud and fail-closed, so it is named here
	// rather than answered with a temp-file-and-rename dance.
	// #nosec G304 -- a fixed file name under the data directory the operator configured
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFilePerm)
	if errors.Is(err, fs.ErrExist) {
		key, found, err = readKey(path)
		if err != nil {
			return nil, path, err
		}
		if !found {
			return nil, path, fmt.Errorf("%s disappeared while it was being created", path)
		}
		return key, path, nil
	}
	if err != nil {
		return nil, path, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err = f.Write(minted); err != nil {
		return nil, path, errors.Join(fmt.Errorf("write %s: %w", path, err), f.Close())
	}
	if err = f.Close(); err != nil {
		return nil, path, fmt.Errorf("write %s: %w", path, err)
	}
	slog.Info("generated a key for this install", "path", path)
	return minted, path, nil
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
