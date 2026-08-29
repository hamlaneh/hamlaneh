package authz

import (
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Phase 2 conference actions (ADR 005). A conference is the first resource in
// this server whose authority is ownership rather than membership, which is
// why it needs a resource of its own rather than a place in the plain
// instance-role switch.
const (
	// ConferenceRevoke is killing a conference link and the meeting behind
	// it: the owner's, or an administrator's.
	ConferenceRevoke Action = "conference:revoke"
	// ConferenceListAll is seeing every conference on the instance rather
	// than only your own. It is decided on the instance role alone — there is
	// no resource yet when the question is asked — and it exists so a handler
	// never reads is_admin to decide what a caller may see.
	ConferenceListAll Action = "conference:list-all"
)

// Conference is the resource for conference-scoped actions: the one fact the
// rules turn on, already loaded by the handler.
//
// There is no Member field and there must not be: a conference has no
// membership, which is the whole feature. What decides authority over the row
// is who made it, and — for the row whose maker is gone — the instance role.
type Conference struct {
	// Owner reports whether the acting user created this conference. It is
	// false when the creating account is gone (conferences.created_by is SET
	// NULL), which is exactly the case an administrator must still be able to
	// revoke.
	Owner bool
}

// NewConference builds the conference resource for one request.
func NewConference(conf storage.Conference, actingUserID uuid.UUID) Conference {
	return Conference{
		Owner: conf.CreatedBy != nil && conf.CreatedBy.ID == actingUserID,
	}
}

// canConference decides the conference-scoped actions.
func canConference(user *storage.User, action Action, res Conference) bool {
	switch action {
	case ConferenceRevoke:
		// Its owner, or an administrator. Everybody else is refused here and
		// answered the same 404 a conference that does not exist gets, so a
		// distinct refusal cannot confirm that one does.
		return res.Owner || user.IsAdmin
	case ConferenceListAll,
		ChannelRead, ChannelUpdate, ChannelMemberAdd, ChannelMemberRemove,
		MessageSend, MessageEdit, MessageDelete, ReadPositionSet, FileUpload, CallJoin,
		AdminUsersList, AdminUsersCreate, AdminUsersUpdate, AdminUsersResetPassword,
		AdminInvitesList, AdminInvitesCreate, AdminInvitesRevoke,
		AdminOrgRead, AdminOrgUpdate, AdminAuditList,
		AdminScimTokensList, AdminScimTokensCreate, AdminScimTokensRevoke:
		// Nothing else is decided against a conference. Listing scope is an
		// instance-role question asked with no resource, and every other
		// action names a resource this is not.
		return false
	default:
		return false
	}
}
