// Package scim serves Hamlaneh's SCIM 2.0 provisioning surface at /scim/v2,
// the door an identity provider's sync engine uses to create, update and
// deactivate accounts. docs/api/scim.md is the contract; ADR 004 is the
// design.
//
// It is a package of its own rather than a file in internal/httpserver
// because the wire format is genuinely separate: its own media type
// (application/scim+json), its own error envelope, and its own resource
// schemas, none of which fit the contract's Error shape or its codegen
// (scim.md §1). It is mounted on the base mux OUTSIDE the contract router's
// security middleware, because the only credential here is a bearer token —
// an administrator's session cookie is worthless at these routes, and a
// provisioning token is worthless under /api (§3).
//
// It is hand-implemented, Users only. The Go SCIM libraries bring their own
// routing and schema machinery and we would still write every line of the
// storage glue (ADR 004).
package scim

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// BasePath is where the whole surface is mounted. Every pattern below spells
// it out, and the parent mux routes on the same prefix, so the two cannot
// drift.
const BasePath = "/scim/v2"

// ContentType is the SCIM media type (RFC 7644 §3.1). Every response this
// package writes carries it, including the errors.
const ContentType = "application/scim+json"

// maxBodyBytes bounds a request body. The largest legitimate one is a User
// resource with a handful of short attributes; 64 KiB is the same bound the
// contract's JSON decoder uses and is far above anything a provider sends.
const maxBodyBytes = 64 << 10

// The single rate-limit budget, spent at the top of the mux (§7).
//
// It cannot live in internal/httpserver's ratelimits.go: that registry is
// keyed on contract route patterns, and these are not contract routes. One
// choke point here means there is no per-route budget to forget — the
// property the fail-closed registry exists to protect, preserved by
// construction instead of by a table.
//
// The number has to clear a provider's initial full sync, which arrives as a
// burst of one request per person rather than a steady trickle: 600 a minute
// absorbs a several-hundred-seat directory landing at once, and is far below
// what a loop against a 256-bit credential would need to be worth running.
// The tokens are 256 bits, so this is hygiene rather than the defence.
const (
	rateLimit  = 600
	rateWindow = time.Minute
)

// Store is everything this package needs from persistent storage.
// *storage.Store implements it.
//
// UpdateUserAdmin is on the list deliberately, and it is the same method the
// admin dashboard calls: the advisory lock, the last-administrator check and
// the revocation of every session family, in one transaction (§5). No second
// kill path exists, and none is needed.
type Store interface {
	ScimTokenByHash(ctx context.Context, tokenHash []byte) (uuid.UUID, error)
	CreateScimUser(ctx context.Context, nu storage.NewScimUser) (storage.User, error)
	ReplaceScimUser(ctx context.Context, id uuid.UUID, attrs storage.ScimUserAttributes) (storage.User, error)
	ListScimUsers(ctx context.Context, f storage.ScimUserFilter, offset, limit int) ([]storage.User, int, error)
	UserByID(ctx context.Context, id uuid.UUID) (storage.User, error)
	UpdateUserAdmin(ctx context.Context, userID uuid.UUID, upd storage.AdminUserUpdate) (storage.User, error)
	OrgSettings(ctx context.Context) (storage.OrgSettings, error)
}

// The production store satisfies the whole surface at compile time.
var _ Store = (*storage.Store)(nil)

// AuditRecorder writes one provisioning action to the append-only log. It is
// internal/audit's own Record shape rather than a third one: this package
// records with no actor — the authority is a credential, not a person — so
// there is nothing for an adapter to translate (§8).
type AuditRecorder interface {
	Record(ctx context.Context, rec audit.Record)
}

// noAudit is the recorder of a service wired without an audit log: a unit
// test fixture, never production. It exists so handlers record
// unconditionally, because a nil check before every event is one chance per
// call site to forget one.
type noAudit struct{}

func (noAudit) Record(context.Context, audit.Record) {}

// Service is the provisioning surface: a handler, its store, its audit
// recorder and its one rate limiter.
type Service struct {
	store   Store
	audit   AuditRecorder
	limiter *ratelimit.Limiter
	mux     *http.ServeMux
}

// Operation is one row of the machine-readable table in scim.md §6: a method
// and the mux pattern serving it. Routes returns them, so the completeness
// gate in internal/authztest can diff what the mux serves against what the
// document declares.
type Operation struct {
	Op      string
	Method  string
	Pattern string
}

func (o Operation) String() string { return o.Method + " " + o.Pattern }

// operations is the whole surface, in one table. The mux is built from it
// and Routes reports it, so a route that exists in code and not in the table
// is impossible rather than merely caught.
func (s *Service) operations() []struct {
	Operation
	handler http.HandlerFunc
} {
	return []struct {
		Operation
		handler http.HandlerFunc
	}{
		{Operation{"serviceProviderConfig", http.MethodGet, BasePath + "/ServiceProviderConfig"}, serveServiceProviderConfig},
		{Operation{"resourceTypes", http.MethodGet, BasePath + "/ResourceTypes"}, serveResourceTypes},
		{Operation{"schemas", http.MethodGet, BasePath + "/Schemas"}, serveSchemas},
		{Operation{"listUsers", http.MethodGet, BasePath + "/Users"}, s.listUsers},
		{Operation{"createUser", http.MethodPost, BasePath + "/Users"}, s.createUser},
		{Operation{"getUser", http.MethodGet, BasePath + "/Users/{id}"}, s.getUser},
		{Operation{"replaceUser", http.MethodPut, BasePath + "/Users/{id}"}, s.replaceUser},
		{Operation{"patchUser", http.MethodPatch, BasePath + "/Users/{id}"}, s.patchUser},
		{Operation{"deleteUser", http.MethodDelete, BasePath + "/Users/{id}"}, s.deleteUser},
	}
}

// Routes returns every operation the mux serves, in the shape scim.md §6
// spells them. The completeness gate diffs it against the document and
// against the authz registry, in both directions.
func Routes() []Operation {
	ops := (&Service{}).operations()
	out := make([]Operation, 0, len(ops))
	for _, op := range ops {
		out = append(out, op.Operation)
	}
	return out
}

// Option configures a Service.
type Option func(*Service)

// WithAudit attaches the recorder that writes the provisioning log. Omitting
// it leaves account mutations unrecorded, which is a test fixture and never
// production.
func WithAudit(rec AuditRecorder) Option {
	return func(s *Service) {
		if rec != nil {
			s.audit = rec
		}
	}
}

// New returns the provisioning service. The returned Service is an
// http.Handler covering everything under /scim/v2, including the paths it
// refuses: an unknown one — /scim/v2/Groups above all — is a SCIM 404 rather
// than the application's, because a sync engine parses this envelope and not
// that one.
func New(store Store, opts ...Option) *Service {
	s := &Service{
		store:   store,
		audit:   noAudit{},
		limiter: ratelimit.New(rateLimit, rateWindow),
		mux:     http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}

	for _, op := range s.operations() {
		s.mux.HandleFunc(op.Method+" "+op.Pattern, op.handler)
	}
	// Everything else under the prefix, /scim/v2/Groups included. Groups are
	// refused by their absence from the ResourceTypes document (§2); this is
	// what a provider that asks anyway actually gets.
	s.mux.HandleFunc(BasePath+"/", func(w http.ResponseWriter, r *http.Request) {
		writeSCIMError(w, r, http.StatusNotFound, "", "no such SCIM resource")
	})

	return s
}

// tokenKey is the unexported context key for the id of the provisioning
// token that authenticated a request. The audit log names which credential
// acted, and that is the only thing it is used for.
type tokenKey struct{}

// tokenFrom returns the authenticating token's id. ok is false only if a
// handler were reached without the authentication middleware, which would be
// a programming error.
func tokenFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(tokenKey{}).(uuid.UUID)
	return id, ok
}

// ServeHTTP resolves the bearer token, spends the single budget, and
// dispatches (§3, §7).
//
// The order is the point, and it is unusual on purpose: the credential is
// resolved BEFORE the budget is spent, and the 401 is written AFTER.
//
//   - Resolving first is what lets the budget be keyed on the credential. A
//     provider's whole sync is one key rather than one per worker address,
//     which is the only key that means anything for an authenticated caller.
//   - Spending before the 401 is what makes guessing at tokens cost
//     something: a request that resolved to nothing spends the client
//     address's budget instead, so a stream of guesses is bounded by where
//     it comes from rather than being free because it failed.
//
// Nothing of a handler has run when either refusal is written.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tokenID, authErr := s.resolveToken(r)
	if authErr != nil && !errors.Is(authErr, storage.ErrNotFound) {
		internalError(w, r, authErr)
		return
	}

	key := "ip:" + clientKey(r)
	if tokenID != uuid.Nil {
		key = "token:" + tokenID.String()
	}
	if s.limiter.Limited(key) {
		writeRateLimited(w, r, s.limiter.RetryAfter(key))
		return
	}
	s.limiter.Record(key)

	if authErr != nil {
		writeUnauthorized(w, r)
		return
	}
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, tokenID)))
}

// resolveToken reads the Authorization: Bearer header and resolves it
// against the live provisioning tokens — and nothing else (§3).
//
// A session cookie, including an administrator's, is not consulted anywhere
// in this package, which is what makes one worthless here. A missing,
// malformed, unknown or revoked token all come back as ErrNotFound, so the
// caller cannot accidentally answer the four of them differently.
//
// It writes no response: the caller owns the refusal, because the budget has
// to be spent between the two.
func (s *Service) resolveToken(r *http.Request) (uuid.UUID, error) {
	raw, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	raw = strings.TrimSpace(raw)
	if !found || !session.PlausibleToken(raw) {
		// The shape check is what keeps the ordering above from turning an
		// unauthenticated stream of guesses into a stream of queries: the
		// budget is spent after this call, so anything that reaches storage
		// reaches it unmetered. A value that could not have come from
		// session.NewToken cannot be a live token, so it is thrown away
		// here — for the same refusal, from the same call site, having cost
		// nothing.
		return uuid.Nil, storage.ErrNotFound
	}
	return s.store.ScimTokenByHash(r.Context(), session.HashToken(raw))
}

// record writes one provisioning action to the audit log. The actor is
// deliberately nil — a credential acted, not a person — and the token's id
// rides in the detail, so the log names WHICH credential (§8).
func (s *Service) record(r *http.Request, action string, target storage.User, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	if tokenID, ok := tokenFrom(r.Context()); ok {
		detail["scim_token_id"] = tokenID.String()
	}

	targetID := target.ID
	label := target.Username
	rec := audit.Record{
		Action:      action,
		TargetID:    &targetID,
		TargetLabel: label,
		Detail:      detail,
	}
	if addr, err := netip.ParseAddr(clientKey(r)); err == nil {
		rec.IP = addr
	}
	s.audit.Record(r.Context(), rec)
}

// clientKey identifies the calling client for the address half of the
// budget and for the audit entry.
//
// It is deliberately the direct peer, with no X-Forwarded-For handling: the
// contract routes have one behind a proxy that strips client-supplied
// values, and a sync engine's address is not a thing this surface makes any
// decision about beyond bounding requests that failed to authenticate.
// Trusting a header here would let a guesser pick their own budget.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		return addr.Unmap().String()
	}
	return host
}

// SCIM error envelope (RFC 7644 §3.12). It is never the application's Error
// shape: a sync engine parses this one, and the two are not interchangeable
// (§1).
type scimError struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// The scimType values this surface uses. RFC 7644 §3.12 fixes the
// vocabulary; these are the members of it that any refusal here can be.
const (
	typeInvalidFilter = "invalidFilter"
	typeInvalidPath   = "invalidPath"
	typeInvalidSyntax = "invalidSyntax"
	typeInvalidValue  = "invalidValue"
	typeUniqueness    = "uniqueness"
	typeTooMany       = "tooMany"
)

// errorSchema is the URN every error envelope carries.
const errorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"

// writeSCIMError sends the SCIM error envelope. scimType is empty for the
// statuses RFC 7644 defines none for (401, 404, 500).
func writeSCIMError(w http.ResponseWriter, r *http.Request, status int, scimType, detail string) {
	writeSCIM(w, r, status, scimError{
		Schemas:  []string{errorSchema},
		Status:   strconv.Itoa(status),
		ScimType: scimType,
		Detail:   detail,
	})
}

// writeUnauthorized is the single source of every refusal a presented
// credential can get: missing, malformed, unknown, revoked, and a session
// cookie offered instead of one. One call site is what keeps them
// byte-identical.
//
// WWW-Authenticate names the scheme, which is what a sync engine reads to
// learn it should be sending a bearer token at all.
func writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="hamlaneh-scim"`)
	writeSCIMError(w, r, http.StatusUnauthorized, "", "a provisioning token is required")
}

// writeRateLimited answers a full budget with the same Retry-After the
// contract's 429 carries, so a provider can back off on a real number
// instead of guessing. Seconds are rounded up, never below 1.
func writeRateLimited(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeSCIMError(w, r, http.StatusTooManyRequests, typeTooMany, "too many requests, try again later")
}

// internalError logs the real cause and answers the generic SCIM 500;
// details never reach the client.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("scim internal error", "method", r.Method, "path", r.URL.Path, "error", err)
	writeSCIMError(w, r, http.StatusInternalServerError, "", "internal server error")
}

// writeSCIM marshals v and sends it with the SCIM media type.
func writeSCIM(w http.ResponseWriter, r *http.Request, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		// Unreachable for these shapes; keep the response well-formed.
		slog.Error("marshal scim response", "path", r.URL.Path, "error", err)
		status = http.StatusInternalServerError
		data = []byte(`{"schemas":["` + errorSchema + `"],"status":"500"}`)
	}

	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		logWriteFailure(r, err)
	}
}

// logWriteFailure records a failed response write. By the time one fails the
// status line is already sent, so logging is all that is left to do.
func logWriteFailure(r *http.Request, err error) {
	slog.Error("write scim response body", "path", r.URL.Path, "error", err)
}

// decodeBody reads a bounded JSON body into dst. Any failure — oversized,
// malformed, wrong field types, trailing content — is 400 invalidSyntax.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeSCIMError(w, r, http.StatusBadRequest, typeInvalidSyntax, "malformed request body")
		return false
	}
	if dec.More() {
		writeSCIMError(w, r, http.StatusBadRequest, typeInvalidSyntax, "malformed request body")
		return false
	}
	return true
}
