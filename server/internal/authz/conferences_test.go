package authz_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// TestCanConference pins ADR 005's revocation authority. Two cells carry the
// weight: an administrator who did not make it may revoke it, and a plain
// member who did not make it may not — the handler answers the second with
// the same 404 a conference that does not exist gets, so this is where the
// distinction actually lives.
func TestCanConference(t *testing.T) {
	t.Parallel()

	member := &storage.User{Username: "member"}
	admin := &storage.User{Username: "admin", IsAdmin: true}

	tests := []struct {
		name   string
		user   *storage.User
		action authz.Action
		res    authz.Conference
		want   bool
	}{
		{"the owner may revoke", member, authz.ConferenceRevoke, authz.Conference{Owner: true}, true},
		{"a stranger may not", member, authz.ConferenceRevoke, authz.Conference{}, false},
		{"an admin may revoke somebody else's", admin, authz.ConferenceRevoke, authz.Conference{}, true},
		// The owner may be gone (created_by is SET NULL), and the row must
		// still be reachable by somebody.
		{"an admin may revoke an ownerless one", admin, authz.ConferenceRevoke, authz.Conference{}, true},
		{"nil user may not revoke their own", nil, authz.ConferenceRevoke, authz.Conference{Owner: true}, false},
		// Listing scope is an instance-role question with no resource; asked
		// against a conference it is a caller bug, and deny is the answer.
		{"list-all is not a conference question", admin, authz.ConferenceListAll, authz.Conference{Owner: true}, false},
		{"a channel action is not a conference question", admin, authz.ChannelRead, authz.Conference{Owner: true}, false},
		{"an admin action is not a conference question", admin, authz.AdminOrgRead, authz.Conference{Owner: true}, false},
		{"an unknown action is denied", admin, authz.Action("conference:bogus"), authz.Conference{Owner: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := authz.Can(context.Background(), tt.user, tt.action, tt.res); got != tt.want {
				t.Errorf("Can(%v, %q, %+v) = %v, want %v", tt.user, tt.action, tt.res, got, tt.want)
			}
		})
	}
}

// TestConferenceListAllIsTheInstanceRole pins the other half: who sees every
// conference rather than only their own. It is asked with no resource,
// because there is none yet when the question is asked.
func TestConferenceListAllIsTheInstanceRole(t *testing.T) {
	t.Parallel()

	if authz.Can(context.Background(), &storage.User{}, authz.ConferenceListAll, nil) {
		t.Error("a plain member may see every conference on the instance")
	}
	if !authz.Can(context.Background(), &storage.User{IsAdmin: true}, authz.ConferenceListAll, nil) {
		t.Error("an administrator cannot see what they may revoke")
	}
	// Revoking needs a conference. Asked with none it is a malformed
	// question, and the safe answer to one is no.
	if authz.Can(context.Background(), &storage.User{IsAdmin: true}, authz.ConferenceRevoke, nil) {
		t.Error("a revocation was allowed with no conference to decide it against")
	}
}

// TestNewConference pins the one fact the rules turn on, including the case
// migration 0016 exists for: an account that is gone leaves no owner behind,
// and nobody inherits the ownership.
func TestNewConference(t *testing.T) {
	t.Parallel()

	me, somebodyElse := uuid.New(), uuid.New()
	owned := storage.Conference{CreatedBy: &storage.ConferenceCreator{ID: me}}

	if !authz.NewConference(owned, me).Owner {
		t.Error("the creator is not the owner of what they created")
	}
	if authz.NewConference(owned, somebodyElse).Owner {
		t.Error("somebody else is the owner")
	}
	if authz.NewConference(storage.Conference{}, me).Owner {
		t.Error("an ownerless conference has an owner")
	}
}
