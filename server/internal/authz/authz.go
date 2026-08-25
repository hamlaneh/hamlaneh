// Package authz is Hamlaneh's single authorization choke point. Every
// resource-level permission decision in the server goes through Can —
// handlers and middleware never inline their own permission logic
// (CLAUDE.md security non-negotiable), and every Can call site is covered
// by the authz matrix tests in internal/authztest.
package authz

import (
	"context"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Action names a permission-checked operation, namespaced
// area:resource:verb.
type Action string

// Phase 1.1 actions. The admin dashboard is the only permissioned surface
// so far; channels and messages arrive in Phase 1.2.
const (
	// AdminUsersList is listing users from the admin dashboard.
	AdminUsersList Action = "admin:users:list"
	// AdminUsersCreate is creating a user from the admin dashboard.
	AdminUsersCreate Action = "admin:users:create"
)

// Can reports whether user may perform action on resource. resource is nil
// for org-level actions; later phases pass channels, messages, and files.
//
// The default is deny: a nil user, an unknown action, or an action the user
// lacks all deny. ctx is unused today but pinned in the signature so
// org-policy lookups can be added without touching every call site.
func Can(_ context.Context, user *storage.User, action Action, resource any) bool {
	if user == nil {
		return false
	}
	switch res := resource.(type) {
	case Channel:
		return canChannel(user, action, res)
	case Message:
		return canMessage(user, action, res)
	}
	switch action {
	case AdminUsersList, AdminUsersCreate:
		return user.IsAdmin
	case ChannelRead, ChannelUpdate, ChannelMemberAdd, ChannelMemberRemove,
		MessageSend, MessageEdit, MessageDelete, ReadPositionSet:
		// Reached only when the caller passed no resource, or one of the
		// wrong type. Every Phase 1.2 action is decided against a channel or
		// a message; without one there is nothing to decide, and deny is the
		// only safe answer to a question that was not fully asked.
		return false
	default:
		return false
	}
}
