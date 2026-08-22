package httpserver

// Budgets the external tests assert against. Mirroring the numbers in a
// test would let the two drift silently — a weakened limit would still
// "pass" — so the tests read the production constants themselves.
const (
	// TotpSettingsRateLimit is the per-account budget shared by the four
	// two-step settings endpoints.
	TotpSettingsRateLimit = totpSettingsRateLimit
)
