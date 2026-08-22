package httpserver

import (
	"errors"
	"net/http"

	openapitypes "github.com/oapi-codegen/runtime/types"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Self-service session management — the settings Sessions list.
//
// Whose sessions these endpoints touch is decided entirely by the session
// that authenticated the request: the principal supplies the user id and the
// current family id, and the storage queries carry the user id as a WHERE
// clause rather than filtering afterwards. Nothing the client sends selects
// an account, and the only client-supplied value here — the family id in the
// path — can never widen that scope.

// ListMySessions returns every device signed in to the caller's account: one
// row per live session family, the family that authenticated this request
// first and then most recently active first. It is deliberately unpaged —
// the set is bounded by the refresh TTL and the design draws a flat list.
func (s *apiServer) ListMySessions(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("list sessions reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	families, err := store.ListSessionFamilies(r.Context(), prin.user.ID, prin.session.FamilyID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	list := api.SessionFamilyList{Sessions: make([]api.SessionFamily, 0, len(families))}
	for _, fam := range families {
		list.Sessions = append(list.Sessions, apiSessionFamily(fam))
	}
	writeJSONValue(w, r, http.StatusOK, list)
}

// RevokeSessionFamily signs one device out. Every generation of the family is
// revoked, so no live refresh token is left behind to mint a new session.
//
// The current family refuses: the design gives that row no sign-out control,
// and signing this device out is logout, which also has to clear its cookies.
// A family that is not the caller's answers 404 rather than 403, so a guessed
// id never confirms that another account holds it.
func (s *apiServer) RevokeSessionFamily(w http.ResponseWriter, r *http.Request, familyID openapitypes.UUID) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("revoke session family reached without principal"))
		return
	}
	if familyID == prin.session.FamilyID {
		writeError(w, r, http.StatusBadRequest, codeCannotRevokeCurrent,
			"this is the current session; use logout to sign this device out")
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	err := store.RevokeUserFamily(r.Context(), prin.user.ID, familyID)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeSessionNotFound, "no such session")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeOtherSessions signs out every device except this one. It answers 204
// whether or not anything else was signed in: the client already knows the
// count, and failing on "nothing to do" would only make the button lie.
func (s *apiServer) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("revoke other sessions reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	if err := store.RevokeOtherFamilies(r.Context(), prin.user.ID, prin.session.FamilyID); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiSessionFamily maps one family onto the contract's SessionFamily.
//
// Location is deliberately left unset. No geolocation source is wired, the
// contract says the field stays null until one is chosen, and the UI renders
// null as "Unknown location" — inventing a value would be worse than saying
// nothing.
func apiSessionFamily(fam storage.SessionFamily) api.SessionFamily {
	out := api.SessionFamily{
		FamilyId:     fam.FamilyID,
		UserAgent:    fam.UserAgent,
		LastActiveAt: fam.LastActiveAt,
		Current:      fam.Current,
	}
	if fam.IP != nil {
		ip := fam.IP.String()
		out.Ip = &ip
	}
	return out
}
