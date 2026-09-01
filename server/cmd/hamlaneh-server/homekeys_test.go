package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
)

// captureWarnings runs fn with the default logger redirected, and returns
// everything it logged.
func captureWarnings(t *testing.T, fn func()) string {
	t.Helper()

	buf := &bytes.Buffer{}
	restore := captureLogs(t, buf)
	defer restore()
	fn()
	return buf.String()
}

// TestShortKeyErrorNamesTheEnvironmentNotAPhantomFile: when the key came from
// the environment, the failure must name the variable. Naming the file sends
// an operator hunting something that does not exist — home mode writes no key
// file at all when the environment supplied one.
func TestShortKeyErrorNamesTheEnvironmentNotAPhantomFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
	}{
		{"file URL key", filesign.EnvKey},
		{"audit key", audit.EnvKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := homeTestEnv(t)
			t.Setenv(tc.env, "too short")

			m, err := homeMode()
			if err != nil {
				t.Fatalf("homeMode() error = %v", err)
			}
			if prepErr := m.prepare(); prepErr != nil {
				t.Fatalf("prepare: %v", prepErr)
			}

			_, _, err = m.keys()
			if err == nil {
				t.Fatal("a key of 9 bytes was accepted")
			}
			if !strings.Contains(err.Error(), tc.env) {
				t.Errorf("error %q does not name %s, the source of the key that failed", err, tc.env)
			}
			if strings.Contains(err.Error(), dir) {
				t.Errorf("error %q points at a key file, but the key came from the environment "+
					"and no file was written", err)
			}
		})
	}
}

// TestShortKeyFileErrorNamesTheFile is the other half: a key that really did
// come from a file is reported against that file.
func TestShortKeyFileErrorNamesTheFile(t *testing.T) {
	dir := homeTestEnv(t)
	path := filepath.Join(dir, auditKeyFile)
	if err := os.WriteFile(path, []byte("too short"), keyFilePerm); err != nil {
		t.Fatalf("write a damaged key file: %v", err)
	}

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if prepErr := m.prepare(); prepErr != nil {
		t.Fatalf("prepare: %v", prepErr)
	}

	_, _, err = m.keys()
	if err == nil {
		t.Fatal("a truncated key file was accepted")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the key file it rejected", err)
	}
}

// TestRejectedAuditKeySaysWhatDeletingItCosts is the footgun this text exists
// for. "This key is too short" invites the obvious fix -- delete it, let a new
// one be generated -- and that permanently orphans every audit entry already
// written. The operator has to be told before they choose, not after.
func TestRejectedAuditKeySaysWhatDeletingItCosts(t *testing.T) {
	dir := homeTestEnv(t)
	if err := os.WriteFile(filepath.Join(dir, auditKeyFile), []byte("too short"), keyFilePerm); err != nil {
		t.Fatalf("write a damaged key file: %v", err)
	}

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if prepErr := m.prepare(); prepErr != nil {
		t.Fatalf("prepare: %v", prepErr)
	}

	_, _, err = m.keys()
	if err == nil {
		t.Fatal("a truncated audit key was accepted")
	}
	for _, want := range []string{"backup", "orphans"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q; deleting the file is the obvious and wrong fix",
				err, want)
		}
	}
}

// TestUnderDir pins the containment check the Windows warning turns on,
// including the boundary that a prefix match alone would get wrong.
func TestUnderDir(t *testing.T) {
	t.Parallel()

	// A real absolute path, so this exercises what filepath.Abs actually
	// produces on this OS rather than a hand-written literal.
	base := t.TempDir()
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"the directory itself", base, true},
		{"inside it", filepath.Join(base, "AppData", "hamlaneh"), true},
		{"case differs", strings.ToUpper(base), runtime.GOOS == "windows"},
		{"a sibling that shares a prefix", base + "two", false},
		{"the parent", filepath.Dir(base), false},
		{"an unrelated absolute path", filepath.Join(filepath.Dir(base), "elsewhere", "hamlaneh"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := underDir(tc.path, base); got != tc.want {
				t.Errorf("underDir(%q, %q) = %v, want %v", tc.path, base, got, tc.want)
			}
		})
	}
}

// TestWindowsWarningOnlyFiresOffProfile: the warning is about inherited ACLs,
// so it must stay quiet for the default location and speak for one outside the
// user profile. On every other OS the mode bits are real and it never fires.
func TestWindowsWarningOnlyFiresOffProfile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the inherited-ACL hazard is Windows-only; elsewhere 0700 is enforced")
	}

	profile := os.Getenv("USERPROFILE")
	if profile == "" {
		t.Skip("no USERPROFILE on this host")
	}

	quiet := captureWarnings(t, func() {
		warnIfUnprotectedOnWindows(filepath.Join(profile, "AppData", "Roaming", homeDirName))
	})
	if strings.Contains(quiet, "outside your Windows user profile") {
		t.Errorf("warned about the default location inside the profile:\n%s", quiet)
	}

	// The volume root of the profile's own drive: "C:\hamlaneh", the exact
	// shape whose inherited ACL lets every local account read the keys.
	// filepath.Join("C:", x) would produce the drive-RELATIVE "C:x" instead.
	volumeRoot := filepath.VolumeName(profile) + string(os.PathSeparator) + "hamlaneh"
	loud := captureWarnings(t, func() {
		warnIfUnprotectedOnWindows(volumeRoot)
	})
	if !strings.Contains(loud, "outside your Windows user profile") {
		t.Errorf("did not warn about a volume-root data directory, where every local account can read the keys:\n%s", loud)
	}
}
