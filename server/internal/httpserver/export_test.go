package httpserver

import (
	"io/fs"
	"net/http"
)

// Budgets the external tests assert against. Mirroring the numbers in a
// test would let the two drift silently — a weakened limit would still
// "pass" — so the tests read the production table itself.
var (
	// TotpSettingsRateLimit is the per-account budget shared by the four
	// two-step settings endpoints.
	TotpSettingsRateLimit = budgetSpecs[budgetTotpSettings].limit
	// SearchRateLimit is the per-account search budget.
	SearchRateLimit = budgetSpecs[budgetSearch].limit
	// MessageSendRateLimit is the per-account message-send budget.
	MessageSendRateLimit = budgetSpecs[budgetMessageSend].limit
	// ConversationWriteRateLimit is the per-account budget shared by channel
	// creation, opening a direct message, and adding a channel member.
	ConversationWriteRateLimit = budgetSpecs[budgetConversationWrite].limit
	// DirectoryRateLimit is the per-account user-directory budget.
	DirectoryRateLimit = budgetSpecs[budgetDirectory].limit
)

// ContentSecurityPolicy is the CSP this binary serves. The
// no-unsafe-sources regression test reads it from here rather than from a
// copy, so weakening the policy cannot be made to "pass" by editing a test
// expectation.
var ContentSecurityPolicy = contentSecurityPolicy

// HandlerWithWebBuild is Handler over an arbitrary web build. The Go CI job
// never runs `npm run build`, so the build embedded in a plain checkout is
// only the placeholder; tests that need a realistic bundle (content-hashed
// assets, public files) supply their own.
func HandlerWithWebBuild(store Store, web fs.FS, opts ...Option) http.Handler {
	return handler(store, web, opts...)
}

// HandlersWithWebBuild is HandlerWithWebBuild for both listeners: the root
// handler New would give the main server and the one it would give the
// admin server. admin is nil unless WithAdminListener is among opts, which
// is what lets one test assert both shapes of the split (ADR 015).
func HandlersWithWebBuild(store Store, web fs.FS, opts ...Option) (main, admin http.Handler) {
	return route(newAPIServer(store, opts...), web)
}
