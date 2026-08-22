// Package mailer delivers Hamlaneh's transactional mail.
//
// The rest of the server depends only on the Mailer interface. Three
// implementations satisfy it: SMTP for a configured instance, Null for the
// zero-config install that has no mail server (it logs and drops), and
// Recorder for tests.
//
// Dispatch is asynchronous: Queue wraps any Mailer with a bounded queue and
// worker goroutines. That is a security requirement, not an optimization —
// an endpoint that answers more slowly when the address exists is an
// account-enumeration oracle whatever its response body says, and SMTP
// latency is measured in seconds.
//
// Message bodies are bilingual (English and Persian) and live in
// templates/. They are the only Persian text in the server; CLAUDE.md's
// language policy exempts webapp locale files alone today and needs an
// amendment covering server-side templates.
package mailer

import (
	"context"
	"errors"
	"log/slog"
)

// Mailer sends transactional mail. Implementations must be safe for
// concurrent use.
type Mailer interface {
	// SendPasswordReset delivers a password-reset link to one address.
	// locale selects the message language ("fa" for Persian, English for
	// anything else). resetURL carries the raw reset token, so it is a
	// secret: no implementation may log it.
	SendPasswordReset(ctx context.Context, to, locale, resetURL string) error
}

// Errors callers branch on with errors.Is.
var (
	// ErrQueueFull reports that asynchronous dispatch was saturated and the
	// message was dropped rather than made to wait.
	ErrQueueFull = errors.New("mailer: dispatch queue is full")
	// ErrQueueClosed reports a send after the queue was shut down.
	ErrQueueClosed = errors.New("mailer: dispatch queue is closed")
	// ErrInvalidAddress reports a recipient that is not a valid RFC 5322
	// address.
	ErrInvalidAddress = errors.New("mailer: invalid email address")
	// ErrNotConfigured reports an incomplete SMTP configuration.
	ErrNotConfigured = errors.New("mailer: incomplete SMTP configuration")
)

// Null is the Mailer of an instance with no mail transport configured: it
// logs that a message was dropped and reports success, so a zero-config
// install still answers every request normally. GET /api/v1/instance
// reports password_reset_available false alongside it, so clients never
// offer a link that cannot arrive.
type Null struct{}

var _ Mailer = Null{}

// SendPasswordReset drops the message, logging the recipient (never the
// link, which carries the raw token) so an administrator can see that mail
// was wanted and is not configured.
func (Null) SendPasswordReset(_ context.Context, to, locale, _ string) error {
	slog.Info("no mail transport configured; dropping password reset mail",
		"to", to, "locale", locale)
	return nil
}
