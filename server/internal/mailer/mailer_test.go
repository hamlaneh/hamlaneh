package mailer_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/mailer"
)

const testTTL = 30 * time.Minute

func TestNullDropsAndSucceeds(t *testing.T) {
	t.Parallel()

	if err := (mailer.Null{}).SendPasswordReset(
		context.Background(), "someone@example.com", "fa", "https://x.test/reset?token=secret",
	); err != nil {
		t.Fatalf("Null.SendPasswordReset: %v", err)
	}
}

func TestNewReturnsNullWhenUnconfigured(t *testing.T) {
	t.Parallel()

	got, err := mailer.New(mailer.Config{}, testTTL)
	if err != nil {
		t.Fatalf("New with zero config: %v", err)
	}
	if _, ok := got.(mailer.Null); !ok {
		t.Fatalf("New with zero config = %T, want mailer.Null", got)
	}
}

func TestConfigConfigured(t *testing.T) {
	t.Parallel()

	if (mailer.Config{}).Configured() {
		t.Error("zero Config reports a transport")
	}
	if !(mailer.Config{Host: "smtp.example.com"}).Configured() {
		t.Error("Config with a host reports no transport")
	}
}

func TestConfigFromEnv(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	tests := []struct {
		name    string
		env     map[string]string
		want    mailer.Config
		wantErr bool
	}{
		{
			name: "unset yields no transport",
			env:  map[string]string{},
			want: mailer.Config{},
		},
		{
			name: "host and from fill submission defaults",
			env: map[string]string{
				mailer.EnvHost: "smtp.example.com",
				mailer.EnvFrom: "hamlaneh@example.com",
			},
			want: mailer.Config{
				Host:       "smtp.example.com",
				Port:       587,
				From:       "hamlaneh@example.com",
				Encryption: mailer.EncryptionStartTLS,
			},
		},
		{
			name: "implicit TLS defaults to 465",
			env: map[string]string{
				mailer.EnvHost:       "smtp.example.com",
				mailer.EnvFrom:       "hamlaneh@example.com",
				mailer.EnvEncryption: "TLS",
			},
			want: mailer.Config{
				Host:       "smtp.example.com",
				Port:       465,
				From:       "hamlaneh@example.com",
				Encryption: mailer.EncryptionTLS,
			},
		},
		{
			name: "explicit port and credentials survive",
			env: map[string]string{
				mailer.EnvHost:       "smtp.example.com",
				mailer.EnvPort:       "2525",
				mailer.EnvFrom:       "hamlaneh@example.com",
				mailer.EnvFromName:   "Hamlaneh",
				mailer.EnvUsername:   "postmaster",
				mailer.EnvPassword:   "not-a-real-password",
				mailer.EnvEncryption: "none",
			},
			want: mailer.Config{
				Host:       "smtp.example.com",
				Port:       2525,
				From:       "hamlaneh@example.com",
				FromName:   "Hamlaneh",
				Username:   "postmaster",
				Password:   "not-a-real-password",
				Encryption: mailer.EncryptionNone,
			},
		},
		{
			name:    "host without a sender is refused",
			env:     map[string]string{mailer.EnvHost: "smtp.example.com"},
			wantErr: true,
		},
		{
			name: "unparseable sender is refused",
			env: map[string]string{
				mailer.EnvHost: "smtp.example.com",
				mailer.EnvFrom: "not an address",
			},
			wantErr: true,
		},
		{
			name: "non-numeric port is refused",
			env: map[string]string{
				mailer.EnvHost: "smtp.example.com",
				mailer.EnvFrom: "hamlaneh@example.com",
				mailer.EnvPort: "submission",
			},
			wantErr: true,
		},
		{
			name: "out-of-range port is refused",
			env: map[string]string{
				mailer.EnvHost: "smtp.example.com",
				mailer.EnvFrom: "hamlaneh@example.com",
				mailer.EnvPort: "70000",
			},
			wantErr: true,
		},
		{
			name: "unknown encryption mode is refused",
			env: map[string]string{
				mailer.EnvHost:       "smtp.example.com",
				mailer.EnvFrom:       "hamlaneh@example.com",
				mailer.EnvEncryption: "ssl",
			},
			wantErr: true,
		},
		{
			name: "username without a password is refused",
			env: map[string]string{
				mailer.EnvHost:     "smtp.example.com",
				mailer.EnvFrom:     "hamlaneh@example.com",
				mailer.EnvUsername: "postmaster",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{
				mailer.EnvHost, mailer.EnvPort, mailer.EnvUsername, mailer.EnvPassword,
				mailer.EnvFrom, mailer.EnvFromName, mailer.EnvEncryption,
			} {
				t.Setenv(name, tc.env[name])
			}

			got, err := mailer.ConfigFromEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ConfigFromEnv = %+v, want an error", got)
				}
				if !errors.Is(err, mailer.ErrNotConfigured) {
					t.Errorf("error %v does not wrap ErrNotConfigured", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfigFromEnv: %v", err)
			}
			if got != tc.want {
				t.Errorf("ConfigFromEnv = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNewSMTPRejectsIncompleteConfig(t *testing.T) {
	t.Parallel()

	if _, err := mailer.NewSMTP(mailer.Config{}, testTTL); !errors.Is(err, mailer.ErrNotConfigured) {
		t.Fatalf("NewSMTP with zero config = %v, want ErrNotConfigured", err)
	}
}

func TestRecorderRecordsAndFails(t *testing.T) {
	t.Parallel()

	var rec mailer.Recorder
	if err := rec.SendPasswordReset(context.Background(), "a@example.com", "fa", "https://x.test/reset?token=t"); err != nil {
		t.Fatalf("SendPasswordReset: %v", err)
	}
	sent := rec.Sent()
	if len(sent) != 1 {
		t.Fatalf("recorded %d messages, want 1", len(sent))
	}
	want := mailer.PasswordResetMail{To: "a@example.com", Locale: "fa", ResetURL: "https://x.test/reset?token=t"}
	if sent[0] != want {
		t.Errorf("recorded %+v, want %+v", sent[0], want)
	}

	sentinel := errors.New("transport down")
	rec.Fail(sentinel)
	if err := rec.SendPasswordReset(context.Background(), "b@example.com", "en", "u"); !errors.Is(err, sentinel) {
		t.Errorf("after Fail, SendPasswordReset = %v, want the installed error", err)
	}
	if len(rec.Sent()) != 1 {
		t.Error("a failing send was still recorded")
	}
}

// containsAll reports whether haystack contains every needle, naming the
// first one it does not.
func containsAll(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			t.Errorf("missing %q in:\n%s", needle, haystack)
		}
	}
}
