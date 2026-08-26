package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Contract bounds for the org-settings screen (openapi.yaml OrgSettings).
// The database CHECKs in migration 0009 are the backstop; these are what
// turn a bad value into the contract's 400 instead of a 500.
const (
	maxOrgNameLen           = 64
	maxSessionLifetimeHours = 8760
)

// AuditRecorder writes one administrative action to the append-only audit
// log. It is declared here because this package is the one that calls it —
// internal/audit implements it, and must be replaceable without touching a
// handler (the same reason Realtime and PreviewEnricher are declared here).
//
// Record returns nothing on purpose. A handler calls it after the change is
// committed, so there is nothing left to undo; telling an admin their change
// failed when it did not — and watching them do it again — is worse than an
// unrecorded entry, which is logged where an operator can see it.
type AuditRecorder interface {
	Record(ctx context.Context, ev AuditEvent)
}

// AuditEvent is one recorded action, in the handlers' own terms. The chain,
// the hash and the sequence are the recorder's business, never a handler's.
//
// "Nobody" is the zero UUID here and a null column in the log; the wiring in
// cmd translates between the two, once, so no call site has to.
type AuditEvent struct {
	// Action is the namespaced verb, e.g. user.deactivated.
	Action string
	// ActorID is the signed-in user who acted. The zero UUID means the
	// system acted rather than a person — which among these handlers only
	// happens on redemption, where the actor is an account that did not
	// exist when the request started.
	ActorID uuid.UUID
	// TargetID is what was acted on; the zero UUID means nothing was.
	TargetID uuid.UUID
	// TargetLabel is what the target was called at the time, kept so the log
	// still reads correctly after a rename or a deletion.
	TargetLabel string
	// Detail is the action's own fields, free-form per action.
	Detail map[string]any
	// IP is the client address the action came from; the zero value records
	// none.
	IP netip.Addr
}

// noAudit is the recorder of a server wired without an audit log — a unit
// test fixture, never production, because main refuses to start without the
// key. It exists so handlers record unconditionally: a nil check before
// every event is one chance per call site to forget one, and the forgotten
// one fails silently.
type noAudit struct{}

func (noAudit) Record(context.Context, AuditEvent) {}

// WithAudit attaches the recorder that writes the audit log.
func WithAudit(rec AuditRecorder) Option {
	return func(s *apiServer) {
		if rec != nil {
			s.audit = rec
		}
	}
}

// WithPublicURL sets the instance's absolute public origin, which is what an
// invitation link is built from. Omitting it leaves invitation links
// site-relative — honest for an install that was never told its own origin,
// and still clickable from the dashboard that shows them.
func WithPublicURL(raw string) Option {
	return func(s *apiServer) { s.publicURL = strings.TrimRight(raw, "/") }
}

// record is the one call site every audited handler goes through. It fills
// in the two things every entry takes from the request rather than from the
// caller: who was signed in, and where they were signed in from.
func (s *apiServer) record(r *http.Request, ev AuditEvent) {
	if prin, ok := principalFrom(r.Context()); ok {
		ev.ActorID = prin.user.ID
	}
	ev.IP, _ = clientIP(r)
	s.audit.Record(r.Context(), ev)
}

// UpdateUserAdmin deactivates, reactivates, or changes a user's role.
//
// Two refusals are the point of the endpoint rather than edge cases:
// deactivating yourself is refused outright, and any change that would leave
// the instance with no admin who can sign in is refused as last_admin. An
// instance nobody can administer is unrecoverable without database access,
// so the dashboard must not be able to produce that state. The last-admin
// rule is decided in the store, under a lock, because it is a fact about a
// set rather than about the row this request names.
func (s *apiServer) UpdateUserAdmin(w http.ResponseWriter, r *http.Request, userID api.UserId) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("admin update reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.UpdateUserAdminRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.IsAdmin == nil && req.IsActive == nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"at least one of is_admin or is_active must be present")
		return
	}

	// Locking yourself out is never the intent the click expressed, and it
	// is refused before the store is asked: the store's last-admin rule
	// would let an admin deactivate themselves while another admin remains,
	// which is exactly the mistake this refusal exists to catch.
	if req.IsActive != nil && !*req.IsActive && userID == prin.user.ID {
		writeError(w, r, http.StatusConflict, codeSelfDeactivation,
			"you cannot deactivate your own account")
		return
	}

	updated, err := store.UpdateUserAdmin(r.Context(), userID, storage.AdminUserUpdate{
		IsAdmin:  req.IsAdmin,
		IsActive: req.IsActive,
	})
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeUserNotFound, "no such user")
		return
	case errors.Is(err, storage.ErrLastAdmin):
		writeError(w, r, http.StatusConflict, codeLastAdmin,
			"the last admin cannot be removed")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	for _, action := range userAdminActions(req, updated) {
		s.record(r, AuditEvent{
			Action:      action,
			TargetID:    updated.ID,
			TargetLabel: updated.Username,
		})
	}
	writeJSONValue(w, r, http.StatusOK, apiAdminUser(updated))
}

// userAdminActions names what one patch actually did, so the log records
// "promoted" and "deactivated" as the separate things they are rather than a
// single "updated" whose meaning has to be reconstructed from a diff. A
// field the request did not mention produces no entry.
func userAdminActions(req api.UpdateUserAdminRequest, updated storage.User) []string {
	actions := make([]string, 0, 2)
	if req.IsAdmin != nil {
		if updated.IsAdmin {
			actions = append(actions, "user.promoted")
		} else {
			actions = append(actions, "user.demoted")
		}
	}
	if req.IsActive != nil {
		if updated.IsActive {
			actions = append(actions, "user.reactivated")
		} else {
			actions = append(actions, "user.deactivated")
		}
	}
	return actions
}

// ForcePasswordReset issues a temporary password and answers with it once —
// this is the only response that will ever carry it, because the server
// keeps an argon2id hash and can never display it again.
//
// The user's session deliberately survives. This is the unlock path for
// somebody who forgot their password, not the offboarding path, and ending
// their session would make the two indistinguishable.
func (s *apiServer) ForcePasswordReset(w http.ResponseWriter, r *http.Request, userID api.UserId) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	// 256 bits from the same generator every other opaque secret here comes
	// from. It is shown once in a copy field, so length costs the admin
	// nothing and guessability would cost the account everything.
	temporary, _ := session.NewToken()

	updated, err := store.SetTemporaryPassword(r.Context(), userID, password.Hash(temporary))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeUserNotFound, "no such user")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{
		Action:      "user.password_reset_forced",
		TargetID:    updated.ID,
		TargetLabel: updated.Username,
	})
	writeJSONValue(w, r, http.StatusOK, api.TemporaryCredentials{
		Username:          updated.Username,
		TemporaryPassword: temporary,
	})
}

// GetOrgSettings returns the instance's settings, including the derived
// count of accounts two-step enforcement would affect.
func (s *apiServer) GetOrgSettings(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}
	settings, err := store.OrgSettings(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiOrgSettings(settings))
}

// UpdateOrgSettings saves the fields the request carries and answers with
// the settings as they now stand. The design has no Save button, so a
// request is usually one field; only the fields present are changed.
func (s *apiServer) UpdateOrgSettings(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.UpdateOrgSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	patch, changed, valid := validateOrgSettings(w, r, req)
	if !valid {
		return
	}

	settings, err := store.UpdateOrgSettings(r.Context(), patch)
	if err != nil {
		internalError(w, r, err)
		return
	}

	if len(changed) > 0 {
		s.record(r, AuditEvent{Action: "org.settings_changed", Detail: changed})
	}
	writeJSONValue(w, r, http.StatusOK, apiOrgSettings(settings))
}

// validateOrgSettings enforces every UpdateOrgSettingsRequest bound the
// contract states, and returns the patch alongside the fields it carries —
// which is what the audit entry records, so the log says which switch moved
// rather than that "settings" changed.
//
// The database CHECKs are the backstop, not the check: a constraint
// violation would surface as a 500, and a bad locale is a 400.
func validateOrgSettings(w http.ResponseWriter, r *http.Request, req api.UpdateOrgSettingsRequest) (storage.OrgSettingsPatch, map[string]any, bool) {
	fail := func(message string) (storage.OrgSettingsPatch, map[string]any, bool) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, message)
		return storage.OrgSettingsPatch{}, nil, false
	}

	var patch storage.OrgSettingsPatch
	changed := map[string]any{}

	if req.OrgName != nil {
		name := strings.TrimSpace(*req.OrgName)
		if n := utf8.RuneCountInString(name); n < 1 || n > maxOrgNameLen {
			return fail("org_name must be 1 to 64 characters")
		}
		patch.OrgName = &name
		changed["org_name"] = name
	}
	if req.DefaultLocale != nil {
		if !req.DefaultLocale.Valid() {
			return fail("default_locale must be one of: en, fa")
		}
		locale := string(*req.DefaultLocale)
		patch.DefaultLocale = &locale
		changed["default_locale"] = locale
	}
	if req.RegistrationMode != nil {
		if !req.RegistrationMode.Valid() {
			return fail("registration_mode must be one of: invite, open")
		}
		mode := string(*req.RegistrationMode)
		patch.RegistrationMode = &mode
		changed["registration_mode"] = mode
	}
	if req.RequireTotp != nil {
		patch.RequireTotp = req.RequireTotp
		changed["require_totp"] = *req.RequireTotp
	}
	if req.SessionLifetimeHours != nil {
		if *req.SessionLifetimeHours < 1 || *req.SessionLifetimeHours > maxSessionLifetimeHours {
			return fail("session_lifetime_hours must be between 1 and 8760")
		}
		patch.SessionLifetimeHours = req.SessionLifetimeHours
		changed["session_lifetime_hours"] = *req.SessionLifetimeHours
	}
	return patch, changed, true
}

// apiAdminUser maps a stored user onto the dashboard's row shape. has_totp
// is deliberately left unset: nothing in this response needs it, and asking
// for it would be a second query per row-action for a field the users table
// never carried.
func apiAdminUser(u storage.User) api.AdminUser {
	out := api.AdminUser{
		Id:                 u.ID,
		Username:           u.Username,
		DisplayName:        u.DisplayName,
		IsAdmin:            u.IsAdmin,
		IsActive:           u.IsActive,
		MustChangePassword: u.MustChangePassword,
		CreatedAt:          u.CreatedAt,
	}
	if u.Email != nil {
		email := *u.Email
		out.Email = &email
	}
	return out
}

// apiOrgSettings maps stored settings onto the contract shape.
func apiOrgSettings(s storage.OrgSettings) api.OrgSettings {
	count := s.AccountsWithoutTotp
	return api.OrgSettings{
		OrgName:              s.OrgName,
		DefaultLocale:        api.OrgSettingsDefaultLocale(s.DefaultLocale),
		RegistrationMode:     api.RegistrationMode(s.RegistrationMode),
		RequireTotp:          s.RequireTotp,
		SessionLifetimeHours: s.SessionLifetimeHours,
		AccountsWithoutTotp:  &count,
	}
}
