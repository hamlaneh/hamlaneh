package scim

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// The User resource: the whole of what this surface implements (scim.md §2).

const (
	userSchemaURN      = "urn:ietf:params:scim:schemas:core:2.0:User"
	listResponseSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
)

// Paging bounds. maxCount is the maxResults the ServiceProviderConfig
// document declares, and the two must agree — a provider reads that number
// and sizes its pages by it.
const (
	defaultCount = 100
	maxCount     = 200
)

// Attribute bounds, taken from the columns they land in (migrations 0001 and
// 0014). They are enforced here so an oversized value is the 400 a provider
// can act on rather than a constraint violation surfacing as a 500.
const (
	maxUserNameLen    = 320
	maxExternalIDLen  = 255
	maxDisplayNameLen = 120
	maxEmailLen       = 320
)

// maxUsernameAttempts bounds the derivation retry. Each attempt is one
// INSERT that lost a uniqueness race on the LOCAL username — the derived
// one, not the provider's — so a directory of a.smith@…, a.smith2@… and so
// on walks a few of these. Twenty is far past any real collision cluster and
// far short of a loop worth waiting on.
//
// Exhausting it is a 409, not a 500: it means every candidate name is taken,
// which is a conflict an operator can act on. It used to be reachable in
// bulk — a value with no usable ASCII derived one shared base, so a Persian
// directory hit this bound at its twenty-first person — and
// uservalidate.DeriveUsername now digests such a value into a base of its
// own, which leaves this bound for genuine clusters only.
const maxUsernameAttempts = 20

// userResource is a User as this surface emits one. Nothing outside §4's
// mapping appears: what is not mapped is not emitted, so nothing can look
// round-trippable that is not.
type userResource struct {
	Schemas     []string     `json:"schemas"`
	ID          string       `json:"id"`
	ExternalID  *string      `json:"externalId,omitempty"`
	UserName    string       `json:"userName"`
	DisplayName string       `json:"displayName,omitempty"`
	Name        *nameValue   `json:"name,omitempty"`
	Emails      []emailValue `json:"emails,omitempty"`
	Active      bool         `json:"active"`
	Meta        metaValue    `json:"meta"`
}

type nameValue struct {
	Formatted string `json:"formatted,omitempty"`
}

type emailValue struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type metaValue struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	LastModified time.Time `json:"lastModified"`
	Location     string    `json:"location"`
}

// listResponse is the RFC 7644 §3.4.2 envelope. Resources is capitalised
// because the specification capitalises it.
type listResponse struct {
	Schemas      []string       `json:"schemas"`
	TotalResults int            `json:"totalResults"`
	StartIndex   int            `json:"startIndex"`
	ItemsPerPage int            `json:"itemsPerPage"`
	Resources    []userResource `json:"Resources"`
}

// userBody is a User as a provider sends one, for create and replace.
//
// There is deliberately no password field. §2 ignores a pushed password, and
// the way to ignore something is not to have somewhere to put it: with no
// field, the decoder drops it before any code could be tempted to use it.
// There is likewise no is_admin, no roles and no groups — this struct is the
// list of what a directory may write.
//
// Active is raw because providers disagree about whether it is a boolean or
// the string "False"; it goes through the same lenient decoder the patch
// path uses.
type userBody struct {
	Schemas     []string        `json:"schemas"`
	ExternalID  *string         `json:"externalId"`
	UserName    string          `json:"userName"`
	DisplayName *string         `json:"displayName"`
	Name        *nameValue      `json:"name"`
	Emails      []emailValue    `json:"emails"`
	Active      json.RawMessage `json:"active"`
}

// userWrite is the account state a create, a replace or a patch produces:
// the mapped attributes plus the one flag that is written through a
// different door.
//
// active is separate from attrs because it is written by UpdateUserAdmin —
// the dashboard's own path, with the last-administrator rule and the session
// revocation inside it — while attrs go through a statement that cannot
// reach is_admin or is_active at all (§5).
type userWrite struct {
	attrs  storage.ScimUserAttributes
	active bool
}

// listUsers answers a provider's directory read: startIndex/count paging and
// the two equality filters §2 supports.
func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	filter, err := parseFilter(query.Get("filter"))
	if err != nil {
		writeSCIMError(w, r, http.StatusBadRequest, typeInvalidFilter,
			`only 'userName eq "..."' and 'externalId eq "..."' are supported`)
		return
	}

	startIndex, count := pageWindow(query.Get("startIndex"), query.Get("count"))

	users, total, err := s.store.ListScimUsers(r.Context(), filter, startIndex-1, count)
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := listResponse{
		Schemas:      []string{listResponseSchema},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(users),
		Resources:    make([]userResource, 0, len(users)),
	}
	for _, u := range users {
		page.Resources = append(page.Resources, resourceOf(u))
	}
	writeSCIM(w, r, http.StatusOK, page)
}

// createUser provisions an account (§4). The provider's userName is stored
// verbatim and the LOCAL username is derived from it, because a directory
// userName is usually an email address and the account rules do not accept
// one — relaxing them would push the change through every screen that
// displays a username.
//
// A userName, externalId or email another account already holds is 409
// uniqueness, which is also the first half of adopting an existing account:
// the provider's next move is a filtered lookup that finds it (§4).
func (s *Service) createUser(w http.ResponseWriter, r *http.Request) {
	var body userBody
	if !decodeBody(w, r, &body) {
		return
	}
	write, ok := s.readBody(w, r, body, userWrite{active: true})
	if !ok {
		return
	}

	settings, err := s.store.OrgSettings(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}

	// The derivation loop: storage owns the uniqueness answer, so a lost
	// race is a retry with the next suffix rather than a pre-flight check
	// that a concurrent create could invalidate between the look and the
	// insert.
	var created storage.User
	for attempt := range maxUsernameAttempts {
		created, err = s.store.CreateScimUser(r.Context(), storage.NewScimUser{
			Username:     uservalidate.DeriveUsername(write.attrs.ScimUserName, attempt),
			ScimUserName: write.attrs.ScimUserName,
			ExternalID:   write.attrs.ExternalID,
			Email:        write.attrs.Email,
			DisplayName:  write.attrs.DisplayName,
			Locale:       settings.DefaultLocale,
			IsActive:     write.active,
		})
		if !errors.Is(err, storage.ErrUsernameTaken) {
			break
		}
	}
	switch {
	case errors.Is(err, storage.ErrScimIdentifierTaken), errors.Is(err, storage.ErrEmailTaken):
		writeConflict(w, r, "an account with that userName, externalId or email already exists")
		return
	case errors.Is(err, storage.ErrUsernameTaken):
		// Every derivation of this userName is already held by somebody.
		// It is still a conflict rather than an internal error, and saying
		// so is the difference between an operator who can act — pick a
		// different userName, or free one of the colliding accounts — and
		// one staring at a 500 with nothing in it.
		writeConflict(w, r,
			"could not derive a free local username from that userName; every candidate is taken")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	s.record(r, "scim.user.created", created, map[string]any{"user_name": write.attrs.ScimUserName})
	w.Header().Set("Location", location(created.ID))
	writeSCIM(w, r, http.StatusCreated, resourceOf(created))
}

// getUser reads one account. The id is the account's own UUID, so a
// malformed one is the same 404 an unknown one gets: both name nothing.
func (s *Service) getUser(w http.ResponseWriter, r *http.Request) {
	user, ok := s.lookup(w, r)
	if !ok {
		return
	}
	writeSCIM(w, r, http.StatusOK, resourceOf(user))
}

// replaceUser is Okta's update style: a full replacement of the mapped
// attributes.
//
// active is the one exception to "replace": when the body omits it, the flag
// is left alone rather than defaulting to false. Replace semantics would
// mean an omitted attribute deactivates the person, and a provider that
// trims its payload would then offboard its whole directory. Deactivation
// has to be something a request asked for.
func (s *Service) replaceUser(w http.ResponseWriter, r *http.Request) {
	user, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var body userBody
	if !decodeBody(w, r, &body) {
		return
	}
	// Everything not carried by the body is cleared — that is what replace
	// means — so the base is empty except for the flag above.
	write, ok := s.readBody(w, r, body, userWrite{active: user.IsActive})
	if !ok {
		return
	}
	s.commit(w, r, user, write)
}

// patchUser is Entra's update style: RFC 7644 operations over the mapped
// attributes, folded onto what the account already has.
func (s *Service) patchUser(w http.ResponseWriter, r *http.Request) {
	user, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var req patchRequest
	if !decodeBody(w, r, &req) {
		return
	}

	write, failure := applyPatch(currentWrite(user), req)
	if failure != nil {
		writeSCIMError(w, r, patchStatus, failure.scimType, failure.detail)
		return
	}
	if msg := validateAttributes(write.attrs); msg != "" {
		writeSCIMError(w, r, http.StatusBadRequest, typeInvalidValue, msg)
		return
	}
	s.commit(w, r, user, write)
}

// deleteUser deactivates (§5). Hard deletion is impossible by schema —
// messages.author_id and attachments.uploader_id are ON DELETE RESTRICT —
// and erasing somebody's history to satisfy a directory would destroy other
// people's conversations.
//
// Repeated deletes answer 204: the resource still exists, deactivated, so
// the operation is idempotent and a provider's retry is not an error.
func (s *Service) deleteUser(w http.ResponseWriter, r *http.Request) {
	user, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if !s.setActive(w, r, user, false) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// commit writes one update: the active flag first, then the attributes.
//
// The order is deliberate. The flag is the offboarding half, and it must not
// be blocked by an attribute conflict that has nothing to do with it — a PUT
// carrying both a deactivation and a userName somebody else now holds still
// ends the access it was sent to end.
func (s *Service) commit(w http.ResponseWriter, r *http.Request, user storage.User, write userWrite) {
	if write.active != user.IsActive {
		if !s.setActive(w, r, user, write.active) {
			return
		}
	}

	updated, err := s.store.ReplaceScimUser(r.Context(), user.ID, write.attrs)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeNotFound(w, r)
		return
	case errors.Is(err, storage.ErrScimIdentifierTaken), errors.Is(err, storage.ErrEmailTaken):
		writeConflict(w, r, "another account already holds that userName, externalId or email")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	if !sameAttributes(write.attrs, currentWrite(user).attrs) {
		s.record(r, "scim.user.updated", updated, nil)
	}
	writeSCIM(w, r, http.StatusOK, resourceOf(updated))
}

// setActive routes the flag through UpdateUserAdmin — the same call the
// admin dashboard makes, which takes the advisory lock, refuses the change
// that would leave nobody able to administer the instance, and revokes every
// session family of an account it deactivates, all in one transaction. The
// WebSocket gateway's existing sweep then closes those sockets, because it
// keys on revoked families rather than on who revoked them (§5).
//
// A refusal is 409 rather than 500: a provider will retry it, and an
// operator needs to be able to read why.
func (s *Service) setActive(w http.ResponseWriter, r *http.Request, user storage.User, active bool) bool {
	if user.IsActive == active {
		return true // already there; nothing to write and nothing to record
	}

	updated, err := s.store.UpdateUserAdmin(r.Context(), user.ID, storage.AdminUserUpdate{IsActive: &active})
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeNotFound(w, r)
		return false
	case errors.Is(err, storage.ErrLastAdmin):
		writeLastAdminConflict(w, r)
		return false
	case err != nil:
		internalError(w, r, err)
		return false
	}

	action := "scim.user.deactivated"
	if active {
		action = "scim.user.reactivated"
	}
	s.record(r, action, updated, nil)
	return true
}

// lookup resolves the {id} path segment to an account, answering the SCIM
// 404 for anything that names nobody.
func (s *Service) lookup(w http.ResponseWriter, r *http.Request) (storage.User, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeNotFound(w, r)
		return storage.User{}, false
	}
	user, err := s.store.UserByID(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeNotFound(w, r)
		return storage.User{}, false
	}
	if err != nil {
		internalError(w, r, err)
		return storage.User{}, false
	}
	return user, true
}

// readBody maps a create or replace body onto the attributes to write, on
// top of base (which carries the active flag the caller decided).
func (s *Service) readBody(w http.ResponseWriter, r *http.Request, body userBody, base userWrite) (userWrite, bool) {
	write := base
	write.attrs = storage.ScimUserAttributes{
		ScimUserName: body.UserName,
		Email:        optional(primaryEmail(body.Emails)),
		DisplayName:  displayNameOf(body),
	}
	if body.ExternalID != nil {
		write.attrs.ExternalID = optional(*body.ExternalID)
	}

	if present(body.Active) {
		active, failure := decodeBool(body.Active)
		if failure != nil {
			writeSCIMError(w, r, http.StatusBadRequest, failure.scimType, failure.detail)
			return userWrite{}, false
		}
		write.active = active
	}

	if msg := validateAttributes(write.attrs); msg != "" {
		writeSCIMError(w, r, http.StatusBadRequest, typeInvalidValue, msg)
		return userWrite{}, false
	}
	return write, true
}

// validateAttributes enforces every bound the columns carry, so an oversized
// attribute is a 400 a provider can act on rather than a constraint
// violation reaching the client as a 500. It returns the refusal, or "".
func validateAttributes(attrs storage.ScimUserAttributes) string {
	switch {
	case attrs.ScimUserName == "":
		return "userName is required"
	case utf8.RuneCountInString(attrs.ScimUserName) > maxUserNameLen:
		return "userName must be at most 320 characters"
	case attrs.ExternalID != nil && utf8.RuneCountInString(*attrs.ExternalID) > maxExternalIDLen:
		return "externalId must be at most 255 characters"
	case utf8.RuneCountInString(attrs.DisplayName) > maxDisplayNameLen:
		return "displayName must be at most 120 characters"
	case attrs.Email != nil && utf8.RuneCountInString(*attrs.Email) > maxEmailLen:
		return "emails value must be at most 320 characters"
	default:
		return ""
	}
}

// currentWrite is the account as a patch starts from: what a no-op patch
// would write back unchanged.
func currentWrite(u storage.User) userWrite {
	w := userWrite{
		attrs: storage.ScimUserAttributes{
			ExternalID:  u.ScimExternalID,
			Email:       u.Email,
			DisplayName: u.DisplayName,
		},
		active: u.IsActive,
	}
	w.attrs.ScimUserName = userNameOf(u)
	return w
}

// resourceOf maps a stored account onto the wire shape (§4).
func resourceOf(u storage.User) userResource {
	res := userResource{
		Schemas:     []string{userSchemaURN},
		ID:          u.ID.String(),
		ExternalID:  u.ScimExternalID,
		UserName:    userNameOf(u),
		DisplayName: u.DisplayName,
		Active:      u.IsActive,
		Meta: metaValue{
			ResourceType: "User",
			Created:      u.CreatedAt,
			LastModified: u.UpdatedAt,
			Location:     location(u.ID),
		},
	}
	if u.DisplayName != "" {
		res.Name = &nameValue{Formatted: u.DisplayName}
	}
	if u.Email != nil {
		res.Emails = []emailValue{{Value: *u.Email, Type: "work", Primary: true}}
	}
	return res
}

// userNameOf is what SCIM calls this account. It is the provider's own value
// when a directory has claimed the account, and the local username
// otherwise — which is what a provider sees when it looks up an account
// somebody created locally, before the PUT that adopts it (§4).
func userNameOf(u storage.User) string {
	if u.ScimUserName != nil && *u.ScimUserName != "" {
		return *u.ScimUserName
	}
	return u.Username
}

// displayNameOf reads §4's two sources in order: displayName, else
// name.formatted.
func displayNameOf(body userBody) string {
	if body.DisplayName != nil {
		return *body.DisplayName
	}
	if body.Name != nil {
		return body.Name.Formatted
	}
	return ""
}

// primaryEmail picks the address §4 maps: the primary entry, else the first.
func primaryEmail(emails []emailValue) string {
	for _, e := range emails {
		if e.Primary && e.Value != "" {
			return e.Value
		}
	}
	for _, e := range emails {
		if e.Value != "" {
			return e.Value
		}
	}
	return ""
}

// location is the resource's own URI, relative to this origin.
func location(id uuid.UUID) string {
	return BasePath + "/Users/" + id.String()
}

// pageWindow reads the two paging parameters. Neither has an error case:
// RFC 7644 §3.4.2.4 makes a startIndex below one mean one, and a provider
// must not have its whole sync fail on a malformed page cursor.
//
// count is the one place a zero is meaningful — the specification allows it
// to ask for totalResults and no resources — so it is kept rather than
// clamped up, while anything negative or unparseable falls back to the
// default page.
func pageWindow(rawStart, rawCount string) (startIndex, count int) {
	startIndex = 1
	if n, err := strconv.Atoi(rawStart); err == nil && n > 1 {
		startIndex = n
	}

	count = defaultCount
	if n, err := strconv.Atoi(rawCount); err == nil && n >= 0 {
		count = n
	}
	if count > maxCount {
		count = maxCount
	}
	return startIndex, count
}

// writeNotFound is the single source of every "no such account" answer.
func writeNotFound(w http.ResponseWriter, r *http.Request) {
	writeSCIMError(w, r, http.StatusNotFound, "", "no such user")
}

// writeConflict answers a 409 that RFC 7644 has a scimType for: another
// account already holds the identifier.
func writeConflict(w http.ResponseWriter, r *http.Request, detail string) {
	writeSCIMError(w, r, http.StatusConflict, typeUniqueness, detail)
}

// writeLastAdminConflict answers the one 409 with no scimType. None of the
// values RFC 7644 defines describes "this would leave nobody able to
// administer the instance", and inventing one a provider cannot look up
// would be worse than the plain status with a detail an operator can read
// (§5). It is a 409 and never a 500 because a provider will retry it.
func writeLastAdminConflict(w http.ResponseWriter, r *http.Request) {
	writeSCIMError(w, r, http.StatusConflict, "",
		"the last active administrator cannot be deactivated")
}

// present reports whether a raw JSON field was carried by the request at
// all. An explicit null counts as absent: a provider that sends one is
// saying it has no value, not that the attribute should be read as false.
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// sameAttributes compares two attribute sets by value. Go's == would compare
// the two *string fields by address, which is never what a caller asking
// "did anything change?" means.
func sameAttributes(a, b storage.ScimUserAttributes) bool {
	return a.ScimUserName == b.ScimUserName &&
		a.DisplayName == b.DisplayName &&
		sameOptional(a.Email, b.Email) &&
		sameOptional(a.ExternalID, b.ExternalID)
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
