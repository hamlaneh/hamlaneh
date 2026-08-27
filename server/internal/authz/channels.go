package authz

import (
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Phase 1.2 actions. Every one is decided against a Channel or Message
// resource; none is decided on the user's instance role alone, because
// membership is the only visibility rule in this phase (ADR 001, and the
// operation descriptions in docs/api/openapi.yaml).
const (
	// ChannelRead is reading a channel, its members, or its history.
	ChannelRead Action = "channel:read"
	// ChannelUpdate is setting a channel's topic.
	ChannelUpdate Action = "channel:update"
	// ChannelMemberAdd is inviting somebody into a channel.
	ChannelMemberAdd Action = "channel:member:add"
	// ChannelMemberRemove is removing somebody other than yourself.
	// Leaving is never checked here: it is always allowed.
	ChannelMemberRemove Action = "channel:member:remove"
	// MessageSend is posting to a channel.
	MessageSend Action = "message:send"
	// MessageEdit is editing an existing message.
	MessageEdit Action = "message:edit"
	// MessageDelete is soft-deleting an existing message.
	MessageDelete Action = "message:delete"
	// ReadPositionSet is moving the caller's own read position.
	ReadPositionSet Action = "channel:read-position:set"
	// FileUpload is uploading a file into a channel. It is its own action
	// rather than a reuse of MessageSend because it happens before any
	// message exists — the composer uploads while the caption is still being
	// typed — and because a file is readable by the channel's members from
	// that moment, whether or not a message ever claims it (ADR 003).
	FileUpload Action = "channel:file:upload"
)

// Channel is the resource for channel-scoped actions: the channel plus the
// two facts about the acting user that the rules turn on.
//
// Can stays a pure function of facts the handler has already loaded. It
// performs no I/O, so it cannot be the reason a permission check is slow,
// and every rule in it is decidable by reading it. The cost is that the
// handler must fetch membership before asking — which it needed anyway to
// answer 404 for a stranger.
type Channel struct {
	Channel storage.Channel
	// Member reports membership as of this request, read from the database
	// rather than from anything captured earlier. Membership changes while a
	// client is connected, and a stale copy is an authorization bug.
	Member bool
	// Creator reports whether the acting user created the channel.
	Creator bool
}

// Message is the resource for message-scoped actions: the containing
// channel's facts plus authorship.
type Message struct {
	Channel Channel
	// Author reports whether the acting user wrote the message.
	Author bool
}

// NewChannel builds the channel resource for one request.
func NewChannel(ch storage.Channel, actingUserID uuid.UUID, member bool) Channel {
	return Channel{
		Channel: ch,
		Member:  member,
		Creator: ch.CreatedBy != nil && *ch.CreatedBy == actingUserID,
	}
}

// canChannel decides the channel-scoped actions.
//
// Membership gates everything, admins included: an org admin who is not in a
// channel is a stranger to it, which is the boundary the contract states for
// every operation and the reason a non-member gets 404 rather than 403.
func canChannel(user *storage.User, action Action, res Channel) bool {
	if !res.Member {
		return false
	}
	switch action {
	case ChannelRead, ChannelUpdate, ChannelMemberAdd, MessageSend, ReadPositionSet, FileUpload:
		// Any member. A direct message's fixed pair and its lack of a topic
		// are refused by the handler as 400s: they are statements about the
		// channel's shape, not about who is asking, and answering 403 would
		// tell a member they lack a permission that does not exist.
		return true
	case ChannelMemberRemove:
		// Removing somebody else: the channel's creator, or an admin who is
		// a member of it. Leaving is always allowed and never reaches here.
		return res.Creator || user.IsAdmin
	case MessageEdit, MessageDelete:
		// Message actions need a Message resource. Asking with a Channel is
		// a caller bug, and the safe answer to a malformed question is no.
		return false
	case AdminUsersList, AdminUsersCreate, AdminUsersUpdate, AdminUsersResetPassword,
		AdminInvitesList, AdminInvitesCreate, AdminInvitesRevoke,
		AdminOrgRead, AdminOrgUpdate, AdminAuditList,
		AdminScimTokensList, AdminScimTokensCreate, AdminScimTokensRevoke:
		// Instance-level actions are not decided by channel membership.
		return false
	default:
		return false
	}
}

// canMessage decides the message-scoped actions.
func canMessage(user *storage.User, action Action, res Message) bool {
	if !res.Channel.Member {
		return false
	}
	switch action {
	case MessageEdit:
		// Author only, admins included. Editing somebody else's words is
		// impersonation, and no role makes that acceptable.
		return res.Author
	case MessageDelete:
		// The author, or an admin who is a member of this channel. Deletion
		// removes words; it does not put new ones in somebody's mouth.
		return res.Author || user.IsAdmin
	case ChannelRead, ChannelUpdate, ChannelMemberAdd, ChannelMemberRemove,
		MessageSend, ReadPositionSet, FileUpload:
		// Channel actions need a Channel resource; see canChannel.
		return false
	case AdminUsersList, AdminUsersCreate, AdminUsersUpdate, AdminUsersResetPassword,
		AdminInvitesList, AdminInvitesCreate, AdminInvitesRevoke,
		AdminOrgRead, AdminOrgUpdate, AdminAuditList,
		AdminScimTokensList, AdminScimTokensCreate, AdminScimTokensRevoke:
		return false
	default:
		return false
	}
}
