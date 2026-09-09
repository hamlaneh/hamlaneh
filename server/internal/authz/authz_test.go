package authz_test

import (
	"context"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

func TestCan(t *testing.T) {
	t.Parallel()

	member := &storage.User{Username: "member"}
	admin := &storage.User{Username: "admin", IsAdmin: true}

	tests := []struct {
		name   string
		user   *storage.User
		action authz.Action
		want   bool
	}{
		{"nil user denied everything", nil, authz.AdminUsersList, false},
		{"member denied admin list", member, authz.AdminUsersList, false},
		{"member denied admin create", member, authz.AdminUsersCreate, false},
		{"admin allowed admin list", admin, authz.AdminUsersList, true},
		{"admin allowed admin create", admin, authz.AdminUsersCreate, true},
		// The update control (ADR 016). Reading it is as much an admin
		// decision as spending a maintenance window on it: what the host last
		// did, and what release is waiting, is instance operations.
		{"member denied the update status", member, authz.AdminUpdateRead, false},
		{"member denied requesting an update", member, authz.AdminUpdateRequest, false},
		{"admin allowed the update status", admin, authz.AdminUpdateRead, true},
		{"admin allowed requesting an update", admin, authz.AdminUpdateRequest, true},
		{"unknown action denied even for admin", admin, authz.Action("bogus:action"), false},
		{"empty action denied", admin, authz.Action(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := authz.Can(context.Background(), tt.user, tt.action, nil); got != tt.want {
				t.Errorf("Can(%v, %q) = %v, want %v", tt.user, tt.action, got, tt.want)
			}
		})
	}
}
