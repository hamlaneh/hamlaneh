package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/bootstrap"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// captureStdout points the announcement at a buffer for the duration of a
// test. The generated password is printed, never logged, so this is where a
// test reads it from.
func captureStdout(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	previous := stdout
	stdout = buf
	t.Cleanup(func() { stdout = previous })
	return buf
}

// captureLogs redirects the default structured logger into buf, so a test can
// assert what did NOT reach it.
func captureLogs(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(previous) }
}

// TestHomeModeMintsAFirstAdmin is gate clause 4's first step and the whole
// point of this slice: a person downloads a binary, runs it with no
// environment at all, and can sign in. Before this they got a warning and no
// way in.
func TestHomeModeMintsAFirstAdmin(t *testing.T) {
	homeTestEnv(t)

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	admin, err := m.firstAdmin()
	if err != nil {
		t.Fatalf("firstAdmin() error = %v", err)
	}

	if !admin.present {
		t.Fatal("home mode reported no admin configuration; the operator would have no way to sign in")
	}
	if !admin.minted {
		t.Error("the password was not generated, so nothing will be shown to the operator")
	}
	if admin.cfg.Username != homeAdminUsername {
		t.Errorf("username = %q, want %q", admin.cfg.Username, homeAdminUsername)
	}

	// It has to survive the same rules the account contract applies to any
	// password, or startup fails on the account it just invented.
	if vErr := uservalidate.Password(admin.cfg.Password); vErr != nil {
		t.Errorf("the generated password fails the account policy: %v", vErr)
	}
	if vErr := uservalidate.Username(admin.cfg.Username); vErr != nil {
		t.Errorf("the generated username fails the account policy: %v", vErr)
	}
}

// TestGeneratedPasswordIsNotAKnownValue is the launch-blocking rule in test
// form (CLAUDE.md, "No default credentials, ever"). A generated secret is
// fine; a knowable one never is, so two fresh installs must not agree.
func TestGeneratedPasswordIsNotAKnownValue(t *testing.T) {
	homeTestEnv(t)

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}

	seen := make(map[string]bool, 32)
	for range 32 {
		admin, adminErr := m.firstAdmin()
		if adminErr != nil {
			t.Fatalf("firstAdmin() error = %v", adminErr)
		}
		if seen[admin.cfg.Password] {
			t.Fatalf("two fresh installs generated the same password (%q); that is a default credential",
				admin.cfg.Password)
		}
		seen[admin.cfg.Password] = true
	}
}

// TestHomeModeKeepsAConfiguredAdmin: an operator who sets the two variables
// the installer sets in server mode gets exactly the old behaviour, and
// nothing is printed, because they already know their password.
func TestHomeModeKeepsAConfiguredAdmin(t *testing.T) {
	homeTestEnv(t)
	t.Setenv(bootstrap.EnvUsername, "amir")
	t.Setenv(bootstrap.EnvPassword, "a password long enough to pass validation")

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	admin, err := m.firstAdmin()
	if err != nil {
		t.Fatalf("firstAdmin() error = %v", err)
	}

	if admin.minted {
		t.Error("generated a password although the operator supplied one")
	}
	if admin.cfg.Username != "amir" || admin.cfg.Password != "a password long enough to pass validation" {
		t.Errorf("the configured admin was not used: %q", admin.cfg.Username)
	}
}

// TestHomeModeCompletesAHalfConfiguredAdmin: half of the pair is not an error
// here the way it is for SMTP or LiveKit, because neither half is dangerous on
// its own — a username with no password gets a generated one, and a password
// with no username gets the default name.
func TestHomeModeCompletesAHalfConfiguredAdmin(t *testing.T) {
	t.Run("username only", func(t *testing.T) {
		homeTestEnv(t)
		t.Setenv(bootstrap.EnvUsername, "amir")

		m, err := homeMode()
		if err != nil {
			t.Fatalf("homeMode() error = %v", err)
		}
		admin, err := m.firstAdmin()
		if err != nil {
			t.Fatalf("firstAdmin() error = %v", err)
		}
		if admin.cfg.Username != "amir" || !admin.minted {
			t.Errorf("username %q, minted %v; want the operator's name and a generated password",
				admin.cfg.Username, admin.minted)
		}
	})

	t.Run("password only", func(t *testing.T) {
		homeTestEnv(t)
		t.Setenv(bootstrap.EnvPassword, "a password long enough to pass validation")

		m, err := homeMode()
		if err != nil {
			t.Fatalf("homeMode() error = %v", err)
		}
		admin, err := m.firstAdmin()
		if err != nil {
			t.Fatalf("firstAdmin() error = %v", err)
		}
		if admin.cfg.Username != homeAdminUsername || admin.minted {
			t.Errorf("username %q, minted %v; want the default name and the operator's password",
				admin.cfg.Username, admin.minted)
		}
	})
}

// TestServerModeMintsNothing is the boundary. Server mode gets its admin from
// deploy/install.sh through the environment, and an unset pair there must
// still be the honest warning it has always been -- not an account this
// process invented for an organization's instance.
func TestServerModeMintsNothing(t *testing.T) {
	homeTestEnv(t)

	admin, err := serverMode().firstAdmin()
	if err != nil {
		t.Fatalf("firstAdmin() error = %v", err)
	}
	if admin.present || admin.minted {
		t.Errorf("server mode invented an admin (present=%v minted=%v)", admin.present, admin.minted)
	}
	if admin.cfg.Username != "" || admin.cfg.Password != "" {
		t.Errorf("server mode filled in credentials: %q", admin.cfg.Username)
	}
}

// TestHomeRunMintsOneAdminAndPrintsItOnce walks what a household user actually
// does: start the binary with no environment, then restart it. The first run
// has to produce an account and show the password; the second must do neither.
//
// The idempotence is the security property. A second run that minted again
// would leave two admins, and one that printed again would be a live
// credential on the console of every restart.
func TestHomeRunMintsOneAdminAndPrintsItOnce(t *testing.T) {
	dir := homeTestEnv(t)
	addr := freeAddr(t)
	t.Setenv(envHomeAddr, addr)

	out := captureStdout(t)
	runHome(t, addr)()

	printed := out.String()
	if !strings.Contains(printed, homeAdminUsername) {
		t.Fatalf("the first run printed no username:\n%s", printed)
	}

	// The password as the operator would read it off their console.
	minted := passwordFrom(t, printed)
	if vErr := uservalidate.Password(minted); vErr != nil {
		t.Fatalf("printed password %q fails the account policy: %v", minted, vErr)
	}

	// It is the password of the account that now exists -- printing one thing
	// and storing another is the failure this asserts away.
	st, err := openHomeStore(context.Background(), filepath.Join(dir, homeDBFile))
	if err != nil {
		t.Fatalf("reopen the home database: %v", err)
	}
	defer st.Close()

	user, err := st.UserByIdentifier(context.Background(), homeAdminUsername)
	if err != nil {
		t.Fatalf("the minted admin is not in the database: %v", err)
	}
	ok, _, err := password.Verify(minted, user.PasswordHash)
	if err != nil || !ok {
		t.Fatalf("the printed password does not open the account it created (ok=%v err=%v)", ok, err)
	}
	if !user.IsAdmin {
		t.Error("the first account is not an admin")
	}
	if !user.MustChangePassword {
		t.Error("the generated password is not forced to be changed on first login")
	}

	// Restart. Nothing new is minted and nothing is printed again.
	out.Reset()
	runHome(t, addr)()

	if second := out.String(); strings.Contains(second, "password") {
		t.Errorf("a restart printed a credential again:\n%s", second)
	}
	count, err := st.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("users after two starts = %d, want 1", count)
	}
}

// failingWriter stands in for a console that cannot be written to.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the console went away") }

// TestAnnouncementFailureStopsStartup: if the password cannot be shown, the
// account exists and nobody will ever know its password. Carrying on would
// leave a healthy-looking server no one can sign in to, so the failure has to
// propagate — and it has to say that deleting the data directory is the way
// back, because a restart will not print anything.
func TestAnnouncementFailureStopsStartup(t *testing.T) {
	dir := homeTestEnv(t)

	previous := stdout
	stdout = failingWriter{}
	t.Cleanup(func() { stdout = previous })

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	admin, err := m.firstAdmin()
	if err != nil {
		t.Fatalf("firstAdmin() error = %v", err)
	}

	err = m.announceFirstAdmin(admin.cfg)
	if err == nil {
		t.Fatal("a console write that failed was reported as a successful announcement")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not name the data directory to delete, which is the only way back", err)
	}
}

// passwordFrom pulls the generated password out of what the console showed,
// the way an operator's eyes do: the line that labels it.
func passwordFrom(t *testing.T, printed string) string {
	t.Helper()

	for _, line := range strings.Split(printed, "\n") {
		_, value, found := strings.Cut(line, "password:")
		if found {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("no password line in the first run's output:\n%s", printed)
	return ""
}

// TestAnnouncementNeverReachesTheLog keeps the generated password out of
// anything that gets collected, rotated or shipped. It goes to the console the
// operator is looking at and nowhere else.
func TestAnnouncementNeverReachesTheLog(t *testing.T) {
	homeTestEnv(t)

	logs := &bytes.Buffer{}
	restore := captureLogs(t, logs)
	defer restore()

	out := captureStdout(t)
	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	admin, err := m.firstAdmin()
	if err != nil {
		t.Fatalf("firstAdmin() error = %v", err)
	}
	if err := m.announceFirstAdmin(admin.cfg); err != nil {
		t.Fatalf("announceFirstAdmin: %v", err)
	}

	if !strings.Contains(out.String(), admin.cfg.Password) {
		t.Error("the announcement did not show the password the operator needs")
	}
	if strings.Contains(logs.String(), admin.cfg.Password) {
		t.Error("the generated password reached the log")
	}
	if _, err := os.Stat(filepath.Join(m.dataDir, "admin-password")); err == nil {
		t.Error("the generated password was written to a file; it must live only on the console")
	}
}
