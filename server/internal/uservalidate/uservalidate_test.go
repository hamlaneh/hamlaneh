package uservalidate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

func TestUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
		wantErr  error // nil means valid
	}{
		{"minimal length", "abc", nil},
		{"digits only", "007", nil},
		{"full character set", "a1_b.c-d", nil},
		{"starts with digit", "9lives", nil},
		{"maximum length", strings.Repeat("a", 32), nil},
		{"empty", "", uservalidate.ErrUsernameLength},
		{"too short", "ab", uservalidate.ErrUsernameLength},
		{"too long", strings.Repeat("a", 33), uservalidate.ErrUsernameLength},
		{"uppercase", "NotLower", uservalidate.ErrUsernamePattern},
		{"starts with dash", "-dash", uservalidate.ErrUsernamePattern},
		{"starts with dot", ".dot", uservalidate.ErrUsernamePattern},
		{"contains space", "has space", uservalidate.ErrUsernamePattern},
		{"non-ascii", "üser", uservalidate.ErrUsernamePattern},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := uservalidate.Username(tt.username); !errors.Is(err, tt.wantErr) {
				t.Errorf("Username(%q) = %v, want %v", tt.username, err, tt.wantErr)
			}
		})
	}
}

func TestPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{"minimal length", strings.Repeat("p", 12), nil},
		{"maximum length", strings.Repeat("p", 1024), nil},
		{"multibyte runes count as one", strings.Repeat("é", 12), nil},
		{"empty", "", uservalidate.ErrPasswordLength},
		{"too short", "elevenchars", uservalidate.ErrPasswordLength},
		{"too long", strings.Repeat("p", 1025), uservalidate.ErrPasswordLength},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := uservalidate.Password(tt.password); !errors.Is(err, tt.wantErr) {
				t.Errorf("Password(len %d) = %v, want %v", len(tt.password), err, tt.wantErr)
			}
		})
	}
}
