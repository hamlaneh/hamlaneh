package httpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	openapitypes "github.com/oapi-codegen/runtime/types"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// Admin-create contract bounds (openapi.yaml AdminCreateUserRequest).
// Username and password bounds live in internal/uservalidate.
const (
	maxEmailLen = 320

	// See displayNameRefusal in text.go for the bound and why it is one.
	maxDisplayNameLen = 120

	defaultListLimit = 50
	maxListLimit     = 100
)

// GetCurrentUser returns the authenticated user. It stays reachable while
// must_change_password is set so the client can render the change screen.
func (s *apiServer) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("users/me reached without principal"))
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiSessionUser(prin.user, prin.session))
}

// UpdateCurrentUser applies the caller's own patch of their account: only
// the fields the request carries change (openapi.yaml updateCurrentUser).
//
// Which account it touches is decided entirely by the session — the id comes
// from the principal, and the request body carries no id to tamper with, so
// there is no account here but the caller's own.
func (s *apiServer) UpdateCurrentUser(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("users/me patch reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.UpdateCurrentUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	upd, ok := validateProfileUpdate(w, r, req)
	if !ok {
		return
	}

	updated, err := store.UpdateUserProfile(r.Context(), prin.user.ID, upd)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiSessionUser(updated, prin.session))
}

// validateProfileUpdate enforces every UpdateCurrentUserRequest bound and
// maps the request onto a storage.UserProfileUpdate. A field the request
// omits stays nil, which is what "only the fields present are changed"
// means by the time it reaches storage. On a violation it answers 400 and
// reports false.
func validateProfileUpdate(w http.ResponseWriter, r *http.Request, req api.UpdateCurrentUserRequest) (storage.UserProfileUpdate, bool) {
	fail := func(message string) (storage.UserProfileUpdate, bool) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, message)
		return storage.UserProfileUpdate{}, false
	}

	var upd storage.UserProfileUpdate
	if req.Locale != nil {
		if !req.Locale.Valid() {
			return fail("locale must be one of: en, fa")
		}
		locale := string(*req.Locale)
		upd.Locale = &locale
	}
	return upd, true
}

// AdminListUsers returns one page of users with keyset cursor pagination.
func (s *apiServer) AdminListUsers(w http.ResponseWriter, r *http.Request, params api.AdminListUsersParams) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("admin list reached without principal"))
		return
	}
	if !authz.Can(r.Context(), &prin.user, authz.AdminUsersList, nil) {
		writeError(w, r, http.StatusForbidden, codeForbidden, msgForbidden)
		return
	}

	limit := defaultListLimit
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > maxListLimit {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "limit must be between 1 and 100")
			return
		}
		limit = *params.Limit
	}

	var after *storage.UserCursor
	if params.Cursor != nil {
		cursor, err := decodeUserCursor(*params.Cursor)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "invalid pagination cursor")
			return
		}
		after = cursor
	}

	// Fetch one row beyond the page to learn whether a next page exists.
	users, err := s.store.ListUsers(r.Context(), storage.ListUsersParams{After: after, Limit: limit + 1})
	if err != nil {
		internalError(w, r, err)
		return
	}

	// The admin shape, not the reduced one: this list feeds the dashboard's
	// users table, which draws an active/inactive column and needs to know
	// who has a second factor.
	page := api.AdminUserPage{Users: make([]api.AdminUser, 0, min(len(users), limit))}
	if len(users) > limit {
		users = users[:limit]
		next := encodeUserCursor(users[len(users)-1])
		page.NextCursor = &next
	}
	for _, u := range users {
		page.Users = append(page.Users, apiAdminUser(u))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// AdminCreateUser creates a user from the dashboard. Created users always
// start with must_change_password set: the admin knows the initial
// password, so it must not outlive first login.
func (s *apiServer) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("admin create reached without principal"))
		return
	}
	if !authz.Can(r.Context(), &prin.user, authz.AdminUsersCreate, nil) {
		writeError(w, r, http.StatusForbidden, codeForbidden, msgForbidden)
		return
	}

	var req api.AdminCreateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	nu, ok := validateCreateUser(w, r, req)
	if !ok {
		return
	}

	nu.PasswordHash = password.Hash(req.Password)

	created, err := s.store.CreateUser(r.Context(), nu)
	switch {
	case errors.Is(err, storage.ErrUsernameTaken):
		writeError(w, r, http.StatusConflict, codeUsernameTaken, "username is already taken")
		return
	case errors.Is(err, storage.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, codeEmailTaken, "email is already taken")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusCreated, apiUser(created))
}

// validateCreateUser enforces every AdminCreateUserRequest contract
// constraint and maps the request onto a storage.NewUser (password hash
// still unset). On a violation it answers 400 and reports false.
func validateCreateUser(w http.ResponseWriter, r *http.Request, req api.AdminCreateUserRequest) (storage.NewUser, bool) {
	fail := func(message string) (storage.NewUser, bool) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, message)
		return storage.NewUser{}, false
	}

	if err := uservalidate.Username(req.Username); err != nil {
		return fail("username " + err.Error())
	}
	if err := uservalidate.Password(req.Password); err != nil {
		return fail("password " + err.Error())
	}

	nu := storage.NewUser{
		Username:           req.Username,
		Locale:             string(api.AdminCreateUserRequestLocaleEn),
		MustChangePassword: true,
	}

	if req.Email != nil {
		email := string(*req.Email)
		// Format was validated by the Email type on decode; bound the length.
		if email == "" || utf8.RuneCountInString(email) > maxEmailLen {
			return fail("email must be 1 to 320 characters")
		}
		nu.Email = &email
	}
	if req.DisplayName != nil {
		if msg := displayNameRefusal(*req.DisplayName); msg != "" {
			return fail(msg)
		}
		nu.DisplayName = *req.DisplayName
	}
	if req.Locale != nil {
		if !req.Locale.Valid() {
			return fail("locale must be one of: en, fa")
		}
		nu.Locale = string(*req.Locale)
	}
	if req.IsAdmin != nil {
		nu.IsAdmin = *req.IsAdmin
	}
	return nu, true
}

// apiSessionUser is apiUser plus the one field that belongs to the caller's
// session rather than the account: totp_enrollment_required. Responses that
// describe the signed-in caller go through here; responses about somebody
// else's account (admin create) have no session in scope and use apiUser,
// whose false is the honest value for "this session".
func apiSessionUser(u storage.User, sess storage.Session) api.User {
	out := apiUser(u)
	out.TotpEnrollmentRequired = sess.TotpEnrollmentRequired
	return out
}

// apiUser maps a storage user onto the contract's User schema. The password
// hash never crosses this boundary.
func apiUser(u storage.User) api.User {
	out := api.User{
		Id:                 u.ID,
		Username:           u.Username,
		DisplayName:        u.DisplayName,
		Locale:             api.UserLocale(u.Locale),
		IsAdmin:            u.IsAdmin,
		MustChangePassword: u.MustChangePassword,
		SsoLinked:          u.SsoLinked,
		CreatedAt:          u.CreatedAt,
	}
	if u.Email != nil {
		email := openapitypes.Email(*u.Email)
		out.Email = &email
	}
	return out
}

// User cursors encode the keyset position (created_at, id) of the last row
// of a page as base64url("RFC3339Nano|uuid"). RFC3339Nano preserves
// PostgreSQL's microsecond precision exactly, so the cursor round-trips.
func encodeUserCursor(u storage.User) string {
	raw := u.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + u.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeUserCursor(encoded string) (*storage.UserCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode cursor: %w", err)
	}
	createdPart, idPart, found := strings.Cut(string(raw), "|")
	if !found {
		return nil, errors.New("decode cursor: missing separator")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdPart)
	if err != nil {
		return nil, fmt.Errorf("decode cursor timestamp: %w", err)
	}
	id, err := uuid.Parse(idPart)
	if err != nil {
		return nil, fmt.Errorf("decode cursor id: %w", err)
	}
	return &storage.UserCursor{CreatedAt: createdAt, ID: id}, nil
}
