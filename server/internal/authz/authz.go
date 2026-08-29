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

// Phase 1.4 actions: the rest of the admin dashboard. Each names one
// route-level decision the security middleware makes; none of them is
// resource-scoped, because an instance admin's authority over an instance
// resource does not vary by which resource it is.
const (
	// AdminUsersUpdate is deactivating, reactivating, or changing a role.
	AdminUsersUpdate Action = "admin:users:update"
	// AdminUsersResetPassword is issuing a temporary password.
	AdminUsersResetPassword Action = "admin:users:reset-password"
	// AdminInvitesList is reading the open invitations.
	AdminInvitesList Action = "admin:invites:list"
	// AdminInvitesCreate is generating an invitation link.
	AdminInvitesCreate Action = "admin:invites:create"
	// AdminInvitesRevoke is closing an open invitation.
	AdminInvitesRevoke Action = "admin:invites:revoke"
	// AdminOrgRead is reading the instance settings.
	AdminOrgRead Action = "admin:org:read"
	// AdminOrgUpdate is changing the instance settings.
	AdminOrgUpdate Action = "admin:org:update"
	// AdminAuditList is reading the audit log.
	AdminAuditList Action = "admin:audit:list"
)

// Phase 1.6 actions: the dashboard half of SCIM provisioning (ADR 004).
// Minting one of these is minting a second credential into the instance, so
// it is an admin decision like every other one on this list.
const (
	// AdminScimTokensList is reading the live provisioning tokens.
	AdminScimTokensList Action = "admin:scim-tokens:list"
	// AdminScimTokensCreate is minting a provisioning token.
	AdminScimTokensCreate Action = "admin:scim-tokens:create"
	// AdminScimTokensRevoke is killing a provisioning token.
	AdminScimTokensRevoke Action = "admin:scim-tokens:revoke"
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
	case Conference:
		return canConference(user, action, res)
	}
	switch action {
	case AdminUsersList, AdminUsersCreate, AdminUsersUpdate, AdminUsersResetPassword,
		AdminInvitesList, AdminInvitesCreate, AdminInvitesRevoke,
		AdminOrgRead, AdminOrgUpdate, AdminAuditList,
		AdminScimTokensList, AdminScimTokensCreate, AdminScimTokensRevoke,
		ConferenceListAll:
		return user.IsAdmin
	case ChannelRead, ChannelUpdate, ChannelMemberAdd, ChannelMemberRemove,
		MessageSend, MessageEdit, MessageDelete, ReadPositionSet, FileUpload, CallJoin,
		ConferenceRevoke:
		// Reached only when the caller passed no resource, or one of the
		// wrong type. Every channel-scoped action is decided against a channel
		// or a message, and revoking a conference against a conference;
		// without one there is nothing to decide, and deny is the only safe
		// answer to a question that was not fully asked.
		return false
	default:
		return false
	}
}
