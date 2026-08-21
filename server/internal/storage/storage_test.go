package storage

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestLatestMigrationVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   []string
		want    uint64
		wantErr string // substring the error must contain; empty means no error
	}{
		{
			name:  "single migration",
			files: []string{"0001_create_users.up.sql", "0001_create_users.down.sql"},
			want:  1,
		},
		{
			name: "highest version wins regardless of order",
			files: []string{
				"0001_create_users.up.sql", "0001_create_users.down.sql",
				"0010_add_index.up.sql", "0010_add_index.down.sql",
				"0002_create_channels.up.sql", "0002_create_channels.down.sql",
			},
			want: 10,
		},
		{
			name:    "no up migrations",
			files:   []string{"notes.txt"},
			wantErr: "no *.up.sql migrations",
		},
		{
			name:    "unparsable version prefix",
			files:   []string{"first_create_users.up.sql"},
			wantErr: "parse version",
		},
		{
			name:    "missing underscore separator",
			files:   []string{"0001.up.sql"},
			wantErr: "NNNN_description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fsys := fstest.MapFS{}
			for _, f := range tt.files {
				fsys["migrations/"+f] = &fstest.MapFile{Data: []byte("SELECT 1;")}
			}

			got, err := latestMigrationVersion(fsys, "migrations")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got error %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got version %d, want %d", got, tt.want)
			}
		})
	}
}

// TestLatestMigrationVersionEmbedded guards the real embedded migration set:
// file names must stay parseable or startup and readiness would break.
func TestLatestMigrationVersionEmbedded(t *testing.T) {
	t.Parallel()

	got, err := latestMigrationVersion(migrationFiles, migrationsDir)
	if err != nil {
		t.Fatalf("embedded migrations are invalid: %v", err)
	}
	if got < 1 {
		t.Errorf("got version %d, want at least 1", got)
	}
}

// TestOpenMissingEnv pins the fail-fast startup contract: without libpq
// environment variables, Open must name exactly the missing variables —
// no more, no less — instead of dialing nowhere.
func TestOpenMissingEnv(t *testing.T) {
	t.Run("all missing", func(t *testing.T) {
		for _, name := range requiredEnv {
			t.Setenv(name, "")
		}

		_, err := Open(context.Background(), "")
		if err == nil {
			t.Fatal("Open with no PG environment returned nil error, want a clear failure")
		}
		want := "missing required PostgreSQL environment variables: " +
			strings.Join(requiredEnv, ", ")
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("error %q does not start with %q", err, want)
		}
	})

	t.Run("partially missing", func(t *testing.T) {
		missing := requiredEnv[0]
		t.Setenv(missing, "")
		for _, name := range requiredEnv[1:] {
			t.Setenv(name, "placeholder")
		}

		_, err := Open(context.Background(), "")
		if err == nil {
			t.Fatal("Open with one missing variable returned nil error, want a clear failure")
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error %q does not name the missing variable %s", err, missing)
		}
		for _, name := range requiredEnv[1:] {
			if strings.Contains(err.Error(), name) {
				t.Errorf("error %q names %s, which is set", err, name)
			}
		}
	})
}

func TestOpenInvalidConnString(t *testing.T) {
	t.Parallel()

	_, err := Open(context.Background(), "://not-a-dsn")
	if err == nil {
		t.Fatal("Open with a malformed connection string returned nil error")
	}
}

// TestOpenUnreachableDatabase proves the retry loop gives up at the context
// deadline with a useful error. Port 1 on localhost refuses immediately.
func TestOpenUnreachableDatabase(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	_, err := Open(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("Open against an unreachable database returned nil error")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("got error %q, want it to mention the database being unreachable", err)
	}
}
