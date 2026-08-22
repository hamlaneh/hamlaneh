package mailer

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Queue sizing. The budget is deliberately small: a self-hosted instance
// sends a trickle of mail, and a queue that can grow without bound is a
// memory-exhaustion lever for anyone who can reach the reset endpoint.
const (
	// QueueSize is how many messages may wait for dispatch.
	QueueSize = 256
	// QueueWorkers is how many messages may be in flight at once.
	QueueWorkers = 2
)

// Queue wraps a Mailer with a bounded queue and worker goroutines, so a
// request never waits for SMTP.
//
// That is a security property, not a nicety: the password-reset endpoint
// must answer in the same time whether or not the address exists, and a
// synchronous SMTP conversation would make the two trivially
// distinguishable. When the queue is full the message is dropped and
// logged rather than made to wait — a saturated mail server must not turn
// into a timing oracle either.
//
// Queue is itself a Mailer, so it composes with any implementation.
type Queue struct {
	mail    Mailer
	jobs    chan resetJob
	timeout time.Duration

	wg sync.WaitGroup

	mu     sync.RWMutex
	closed bool
}

var _ Mailer = (*Queue)(nil)

// resetJob is one queued password-reset message. The context is detached
// from the request that produced it: the response returns long before the
// mail is sent, so the request's cancellation must not kill the delivery.
type resetJob struct {
	ctx      context.Context
	to       string
	locale   string
	resetURL string
}

// NewQueue starts a Queue dispatching through mail with size slots and
// workers goroutines. Call Close at shutdown to drain it.
func NewQueue(mail Mailer, size, workers int) *Queue {
	if size < 1 {
		size = 1
	}
	if workers < 1 {
		workers = 1
	}
	q := &Queue{
		mail:    mail,
		jobs:    make(chan resetJob, size),
		timeout: SendTimeout,
	}
	q.wg.Add(workers)
	for range workers {
		go q.work()
	}
	return q
}

// SendPasswordReset enqueues the message and returns immediately. A full
// queue drops the message and reports ErrQueueFull; a closed queue reports
// ErrQueueClosed. Callers must treat both as "logged, not shown": telling
// the requester that their message was dropped would reveal that their
// address matched an account.
func (q *Queue) SendPasswordReset(ctx context.Context, to, locale, resetURL string) error {
	job := resetJob{
		ctx:      context.WithoutCancel(ctx),
		to:       to,
		locale:   locale,
		resetURL: resetURL,
	}

	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return ErrQueueClosed
	}

	select {
	case q.jobs <- job:
		return nil
	default:
		slog.Warn("mail dispatch queue is full; dropping password reset mail", "to", to)
		return ErrQueueFull
	}
}

// Close stops accepting messages and waits for the queued ones to be
// dispatched. It is safe to call more than once.
func (q *Queue) Close() {
	q.mu.Lock()
	alreadyClosed := q.closed
	q.closed = true
	if !alreadyClosed {
		close(q.jobs)
	}
	q.mu.Unlock()

	q.wg.Wait()
}

// work dispatches queued messages until the queue is closed and drained.
func (q *Queue) work() {
	defer q.wg.Done()

	for job := range q.jobs {
		ctx, cancel := context.WithTimeout(job.ctx, q.timeout)
		err := q.mail.SendPasswordReset(ctx, job.to, job.locale, job.resetURL)
		cancel()
		if err != nil {
			// The recipient is logged; the link is not — it carries the
			// raw token.
			slog.Error("send password reset mail", "to", job.to, "error", err)
		}
	}
}
