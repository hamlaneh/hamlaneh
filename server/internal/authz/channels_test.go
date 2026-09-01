package authz_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// channelActions is every Phase 1.2 channel-scoped action. Tests iterate it
// so a new action added without a rule shows up as a failure here rather
// than as a quiet deny somewhere in a handler.
var channelActions = []authz.Action{
	authz.ChannelRead, authz.ChannelUpdate, authz.ChannelMemberAdd,
	authz.ChannelMemberRemove, authz.MessageSend, authz.ReadPositionSet,
	authz.MlsRead, authz.MlsWrite,
}

func TestCanChannel(t *testing.T) {
	t.Parallel()

	creatorID := uuid.New()
	member := &storage.User{Username: "member"}
	admin := &storage.User{Username: "admin", IsAdmin: true}
	channel := storage.Channel{ID: uuid.New(), Kind: storage.ChannelKindPrivate, CreatedBy: &creatorID}

	// The resource the handler builds: the channel, plus the two facts the
	// rules turn on.
	asMember := authz.Channel{Channel: channel, Member: true}
	asCreator := authz.Channel{Channel: channel, Member: true, Creator: true}
	asStranger := authz.Channel{Channel: channel}

	tests := []struct {
		name   string
		user   *storage.User
		action authz.Action
		res    authz.Channel
		want   bool
	}{
		{"member reads", member, authz.ChannelRead, asMember, true},
		{"member sets the topic", member, authz.ChannelUpdate, asMember, true},
		{"member invites", member, authz.ChannelMemberAdd, asMember, true},
		{"member sends", member, authz.MessageSend, asMember, true},
		{"member moves its own read position", member, authz.ReadPositionSet, asMember, true},

		// The E2EE transport (ADR 006). Both are any-member, which is the
		// ADR's transport rule: who can actually decrypt is the group's own
		// business, and this server cannot enforce it without reading group
		// state it is designed to be unable to read. They are two actions so
		// a later role model has somewhere to separate them.
		{"member reads the group", member, authz.MlsRead, asMember, true},
		{"member moves the group", member, authz.MlsWrite, asMember, true},

		// Removing somebody else is the one channel action membership alone
		// does not buy.
		{"member cannot remove another member", member, authz.ChannelMemberRemove, asMember, false},
		{"creator removes another member", member, authz.ChannelMemberRemove, asCreator, true},
		{"admin member removes another member", admin, authz.ChannelMemberRemove, asMember, true},

		// The boundary the contract states in every operation: an org admin
		// holds no power in a channel they are not in.
		{"admin non-member cannot read", admin, authz.ChannelRead, asStranger, false},
		{"admin non-member cannot remove", admin, authz.ChannelMemberRemove, asStranger, false},

		// Message actions need a Message resource; asked with a Channel they
		// are a malformed question, and the answer to those is no.
		{"edit asked with a channel resource", member, authz.MessageEdit, asMember, false},
		{"delete asked with a channel resource", admin, authz.MessageDelete, asMember, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := authz.Can(context.Background(), tt.user, tt.action, tt.res); got != tt.want {
				t.Errorf("Can(%s) = %v, want %v", tt.action, got, tt.want)
			}
		})
	}
}

// TestChannelActionsDenyNonMembers is the IDOR floor, asserted over every
// channel action at once rather than one row per action: a stranger to a
// channel can do nothing to it, whoever they are.
func TestChannelActionsDenyNonMembers(t *testing.T) {
	t.Parallel()

	stranger := authz.Channel{Channel: storage.Channel{ID: uuid.New()}}
	for _, user := range []*storage.User{
		{Username: "member"},
		{Username: "admin", IsAdmin: true},
	} {
		for _, action := range channelActions {
			if authz.Can(context.Background(), user, action, stranger) {
				t.Errorf("%s allowed %s on a channel they are not in", user.Username, action)
			}
		}
	}
}

func TestCanMessage(t *testing.T) {
	t.Parallel()

	member := &storage.User{Username: "member"}
	admin := &storage.User{Username: "admin", IsAdmin: true}
	inChannel := authz.Channel{Channel: storage.Channel{ID: uuid.New()}, Member: true}

	mine := authz.Message{Channel: inChannel, Author: true}
	theirs := authz.Message{Channel: inChannel}
	outsideChannel := authz.Message{Channel: authz.Channel{Channel: storage.Channel{ID: uuid.New()}}, Author: true}

	tests := []struct {
		name   string
		user   *storage.User
		action authz.Action
		res    authz.Message
		want   bool
	}{
		{"author edits their own", member, authz.MessageEdit, mine, true},
		{"author deletes their own", member, authz.MessageDelete, mine, true},

		// Editing is author-only, admins included: putting words in somebody
		// else's mouth is impersonation and no role makes it acceptable.
		{"member cannot edit another's", member, authz.MessageEdit, theirs, false},
		{"admin member cannot edit another's", admin, authz.MessageEdit, theirs, false},

		// Deleting is different: it removes words rather than inventing them.
		{"member cannot delete another's", member, authz.MessageDelete, theirs, false},
		{"admin member deletes another's", admin, authz.MessageDelete, theirs, true},

		// Membership still gates everything, authorship included: a message
		// written before the author was removed is no longer theirs to touch.
		{"author outside the channel cannot edit", member, authz.MessageEdit, outsideChannel, false},
		{"admin outside the channel cannot delete", admin, authz.MessageDelete, outsideChannel, false},

		// Channel actions asked with a Message resource are the mirror of
		// the two rows above them in TestCanChannel: a malformed question,
		// and the safe answer to one is no.
		{"mls read asked with a message resource", member, authz.MlsRead, mine, false},
		{"mls write asked with a message resource", admin, authz.MlsWrite, mine, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := authz.Can(context.Background(), tt.user, tt.action, tt.res); got != tt.want {
				t.Errorf("Can(%s) = %v, want %v", tt.action, got, tt.want)
			}
		})
	}
}

// TestNewChannelReadsCreatorFromTheRow guards the one derived fact: Creator
// must come from the channel row, never from anything the caller asserts.
func TestNewChannelReadsCreatorFromTheRow(t *testing.T) {
	t.Parallel()

	creator, other := uuid.New(), uuid.New()
	channel := storage.Channel{ID: uuid.New(), CreatedBy: &creator}

	if res := authz.NewChannel(channel, creator, true); !res.Creator {
		t.Error("the creator was not recognised as one")
	}
	if res := authz.NewChannel(channel, other, true); res.Creator {
		t.Error("a non-creator was treated as the creator")
	}
	// A channel whose creator was deleted (created_by is ON DELETE SET NULL)
	// has no creator, and nobody inherits the role.
	if res := authz.NewChannel(storage.Channel{ID: uuid.New()}, other, true); res.Creator {
		t.Error("a channel with no creator granted creator rights")
	}
}

// TestMismatchedQuestionsDeny covers the branches that exist only to make
// the switches exhaustive. They are not decoration: the linter forces every
// action to be named in every switch precisely so adding one cannot fall
// through to a silent answer, and these cases prove the answer is no.
func TestMismatchedQuestionsDeny(t *testing.T) {
	t.Parallel()

	admin := &storage.User{Username: "admin", IsAdmin: true}
	inChannel := authz.Channel{Channel: storage.Channel{ID: uuid.New()}, Member: true}
	message := authz.Message{Channel: inChannel, Author: true}

	cases := []struct {
		name     string
		action   authz.Action
		resource any
	}{
		// An instance-level action asked against a channel or a message:
		// admin rights are not granted by being in a conversation.
		{"admin list against a channel", authz.AdminUsersList, inChannel},
		{"admin create against a channel", authz.AdminUsersCreate, inChannel},
		{"admin list against a message", authz.AdminUsersList, message},

		// A channel action asked against a message resource.
		{"channel read against a message", authz.ChannelRead, message},
		{"member add against a message", authz.ChannelMemberAdd, message},

		// A channel or message action asked with no resource at all: the
		// question was never fully put, so it cannot be answered yes.
		{"channel read with no resource", authz.ChannelRead, nil},
		{"message delete with no resource", authz.MessageDelete, nil},
		{"read position with no resource", authz.ReadPositionSet, nil},

		// An action nobody defined, against each resource shape.
		{"unknown action against a channel", authz.Action("bogus:action"), inChannel},
		{"unknown action against a message", authz.Action("bogus:action"), message},

		// Note what is deliberately NOT here: an instance action carrying an
		// unrelated resource still answers on the instance rule. Can's
		// contract is that resource is meaningless for org-level actions, so
		// an admin listing users is right whatever junk came with the call.
		// The dangerous direction is the other one — a channel action with
		// the wrong resource — and the nil cases above pin that at deny.
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if authz.Can(context.Background(), admin, tc.action, tc.resource) {
				t.Errorf("Can(%s) allowed a question that was not properly asked", tc.action)
			}
		})
	}
}
