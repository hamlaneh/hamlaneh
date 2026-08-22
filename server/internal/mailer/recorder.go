package mailer

import (
	"context"
	"sync"
	"time"
)

// PasswordResetMail is one message a Recorder captured.
type PasswordResetMail struct {
	To       string
	Locale   string
	ResetURL string
}

// Recorder is a Mailer that records what it was asked to send instead of
// sending it. It lives in the production package rather than a _test file
// because the httpserver and passwordreset tests need it too, and a fake
// that drifts from the interface it fakes is worse than no fake at all.
//
// It is safe for concurrent use, which matters: dispatch is asynchronous,
// so the worker goroutine records while the test goroutine reads.
type Recorder struct {
	mu   sync.Mutex
	sent []PasswordResetMail
	fail error
}

var _ Mailer = (*Recorder)(nil)

// SendPasswordReset records the message, or returns the error a previous
// Fail call installed.
func (r *Recorder) SendPasswordReset(_ context.Context, to, locale, resetURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, PasswordResetMail{To: to, Locale: locale, ResetURL: resetURL})
	return nil
}

// Fail makes every later send return err. Passing nil restores recording.
func (r *Recorder) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}

// Sent returns a copy of everything recorded so far.
func (r *Recorder) Sent() []PasswordResetMail {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]PasswordResetMail(nil), r.sent...)
}

// WaitFor waits for at least n recorded messages and returns everything
// recorded, or gives up after timeout and returns what there is — the
// caller asserts on the length and reports the failure with its own
// context.
func (r *Recorder) WaitFor(n int, timeout time.Duration) []PasswordResetMail {
	const poll = 2 * time.Millisecond

	deadline := time.Now().Add(timeout)
	for {
		if got := r.Sent(); len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(poll)
	}
}
