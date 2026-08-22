package mailer_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/mailer"
)

// blockingMailer parks inside SendPasswordReset until it is released. It is
// how the tests below observe that dispatch really is off the caller's
// goroutine.
type blockingMailer struct {
	entered chan struct{}
	release chan struct{}
}

func newBlockingMailer() *blockingMailer {
	return &blockingMailer{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (b *blockingMailer) SendPasswordReset(_ context.Context, _, _, _ string) error {
	b.entered <- struct{}{}
	<-b.release
	return nil
}

// TestQueueDoesNotBlockTheCaller is the enumeration defense in test form: a
// transport that never answers must not delay the request that triggered
// it.
func TestQueueDoesNotBlockTheCaller(t *testing.T) {
	t.Parallel()

	blocking := newBlockingMailer()
	queue := mailer.NewQueue(blocking, 4, 1)
	t.Cleanup(func() {
		close(blocking.release)
		queue.Close()
	})

	start := time.Now()
	if err := queue.SendPasswordReset(context.Background(), "a@example.com", "en", "https://x.test/reset?token=t"); err != nil {
		t.Fatalf("SendPasswordReset: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("enqueueing took %v; dispatch is not asynchronous", elapsed)
	}
	<-blocking.entered
}

// TestQueueDropsWhenFull pins the choice to drop rather than wait: a
// saturated mail server must not become a timing oracle either.
func TestQueueDropsWhenFull(t *testing.T) {
	t.Parallel()

	blocking := newBlockingMailer()
	queue := mailer.NewQueue(blocking, 1, 1)
	t.Cleanup(func() {
		close(blocking.release)
		queue.Close()
	})

	ctx := context.Background()
	if err := queue.SendPasswordReset(ctx, "first@example.com", "en", "u"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	// The single worker is now parked inside the transport, so the one
	// buffer slot is free again.
	<-blocking.entered

	if err := queue.SendPasswordReset(ctx, "second@example.com", "en", "u"); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if err := queue.SendPasswordReset(ctx, "third@example.com", "en", "u"); !errors.Is(err, mailer.ErrQueueFull) {
		t.Fatalf("third send = %v, want ErrQueueFull", err)
	}
}

func TestQueueRefusesSendsAfterClose(t *testing.T) {
	t.Parallel()

	var recorder mailer.Recorder
	queue := mailer.NewQueue(&recorder, 4, 1)
	queue.Close()
	queue.Close() // idempotent

	err := queue.SendPasswordReset(context.Background(), "a@example.com", "en", "u")
	if !errors.Is(err, mailer.ErrQueueClosed) {
		t.Fatalf("send after Close = %v, want ErrQueueClosed", err)
	}
}

// TestQueueDeliversAfterRequestContextIsCanceled proves the worker runs on
// a detached context: the HTTP response returns — canceling the request —
// long before the message leaves.
func TestQueueDeliversAfterRequestContextIsCanceled(t *testing.T) {
	t.Parallel()

	var recorder mailer.Recorder
	queue := mailer.NewQueue(&recorder, 4, 1)

	ctx, cancel := context.WithCancel(context.Background())
	if err := queue.SendPasswordReset(ctx, "a@example.com", "fa", "https://x.test/reset?token=t"); err != nil {
		t.Fatalf("SendPasswordReset: %v", err)
	}
	cancel()
	queue.Close()

	sent := recorder.Sent()
	if len(sent) != 1 {
		t.Fatalf("delivered %d messages, want 1", len(sent))
	}
	want := mailer.PasswordResetMail{To: "a@example.com", Locale: "fa", ResetURL: "https://x.test/reset?token=t"}
	if sent[0] != want {
		t.Errorf("delivered %+v, want %+v", sent[0], want)
	}
}

// TestQueueCloseDrains keeps Close honest: queued messages are sent, not
// discarded.
func TestQueueCloseDrains(t *testing.T) {
	t.Parallel()

	var recorder mailer.Recorder
	queue := mailer.NewQueue(&recorder, mailer.QueueSize, mailer.QueueWorkers)

	const count = 20
	for i := range count {
		if err := queue.SendPasswordReset(context.Background(), "a@example.com", "en", strconv.Itoa(i)); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	queue.Close()

	if got := len(recorder.Sent()); got != count {
		t.Errorf("delivered %d messages, want %d", got, count)
	}
}
