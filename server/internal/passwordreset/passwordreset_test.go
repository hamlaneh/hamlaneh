package passwordreset_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/mailer"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

const (
	baseURL  = "https://chat.example.com"
	waitMail = 3 * time.Second
)

// createdToken is one CreatePasswordResetToken call the fake saw.
type createdToken struct {
	userID uuid.UUID
	hash   []byte
	ttl    time.Duration
}

// fakeStore is the storage half of the service, wired per test.
type fakeStore struct {
	mu sync.Mutex

	user    storage.User
	userErr error

	created   []createdToken
	createErr error

	consumedHash     []byte
	consumedPassword string
	consumeUser      uuid.UUID
	outcome          storage.ResetOutcome
	consumeErr       error

	lookedUp []string
}

func (f *fakeStore) UserByEmail(_ context.Context, email string) (storage.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookedUp = append(f.lookedUp, email)
	if f.userErr != nil {
		return storage.User{}, f.userErr
	}
	return f.user, nil
}

func (f *fakeStore) CreatePasswordResetToken(_ context.Context, userID uuid.UUID, hash []byte, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, createdToken{userID: userID, hash: hash, ttl: ttl})
	return nil
}

func (f *fakeStore) ConsumePasswordReset(_ context.Context, hash []byte, passwordHash string) (uuid.UUID, storage.ResetOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumedHash = hash
	f.consumedPassword = passwordHash
	if f.consumeErr != nil {
		return uuid.Nil, storage.ResetOutcomeUnknown, f.consumeErr
	}
	return f.consumeUser, f.outcome, nil
}

func (f *fakeStore) tokens() []createdToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]createdToken(nil), f.created...)
}

func (f *fakeStore) lookups() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lookedUp...)
}

// knownUser is the account the fake resolves an address to.
func knownUser(email, locale string) storage.User {
	return storage.User{ID: uuid.New(), Username: "known", Email: &email, Locale: locale}
}

// newService wires a service over the fake store and a recording mailer.
func newService(t *testing.T, store *fakeStore, cfg passwordreset.Config) (*passwordreset.Service, *mailer.Recorder) {
	t.Helper()

	var recorder mailer.Recorder
	svc, err := passwordreset.New(store, &recorder, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, &recorder
}

func TestNewRequiresAPublicURLWhenMailIsDeliverable(t *testing.T) {
	t.Parallel()

	_, err := passwordreset.New(&fakeStore{}, &mailer.Recorder{}, passwordreset.Config{Deliverable: true})
	if err == nil {
		t.Fatal("New accepted a deliverable configuration with no public base URL")
	}
	if !strings.Contains(err.Error(), passwordreset.EnvPublicURL) {
		t.Errorf("error %q does not name %s", err, passwordreset.EnvPublicURL)
	}
}

func TestNewWithoutMailStaysConstructibleAndUnavailable(t *testing.T) {
	t.Parallel()

	svc, _ := newService(t, &fakeStore{}, passwordreset.Config{})
	if svc.Available() {
		t.Error("a service with no transport reports itself available")
	}
}

func TestNewRejectsUnusableBaseURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"no scheme", "chat.example.com"},
		{"wrong scheme", "ftp://chat.example.com"},
		{"no host", "https://"},
		{"carries a query", "https://chat.example.com?a=b"},
		{"carries a fragment", "https://chat.example.com#top"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, err := passwordreset.New(&fakeStore{}, &mailer.Recorder{},
				passwordreset.Config{BaseURL: tc.raw, Deliverable: true})
			if err == nil {
				svc.Close()
				t.Fatalf("New accepted %q as a public base URL", tc.raw)
			}
		})
	}
}

func TestRequestMailsTheAccountItFound(t *testing.T) {
	t.Parallel()

	store := &fakeStore{user: knownUser("Owner@Example.com", "fa")}
	svc, recorder := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	// Presented in a different case than the account stores.
	if err := svc.Request(context.Background(), "203.0.113.7", "OWNER@example.com"); err != nil {
		t.Fatalf("Request: %v", err)
	}

	sent := recorder.WaitFor(1, waitMail)
	if len(sent) != 1 {
		t.Fatalf("dispatched %d messages, want 1", len(sent))
	}
	if sent[0].To != "Owner@Example.com" {
		t.Errorf("mailed %q, want the address stored on the account", sent[0].To)
	}
	if sent[0].Locale != "fa" {
		t.Errorf("locale %q, want the account's fa", sent[0].Locale)
	}

	link, err := url.Parse(sent[0].ResetURL)
	if err != nil {
		t.Fatalf("parse reset URL %q: %v", sent[0].ResetURL, err)
	}
	if link.Scheme != "https" || link.Host != "chat.example.com" || link.Path != "/reset" {
		t.Errorf("reset URL = %q, want %s/reset", sent[0].ResetURL, baseURL)
	}

	// The token must ride in the fragment and ONLY the fragment: fragments
	// are never sent to a server, so the live token cannot land in access
	// logs or a Referer header. A token in the query string would.
	if link.RawQuery != "" {
		t.Errorf("reset URL %q carries a query string; the token must live in the fragment", sent[0].ResetURL)
	}
	raw, ok := strings.CutPrefix(link.Fragment, "token=")
	if !ok || raw == "" {
		t.Fatalf("reset URL %q carries no #token= fragment", sent[0].ResetURL)
	}

	tokens := store.tokens()
	if len(tokens) != 1 {
		t.Fatalf("stored %d tokens, want 1", len(tokens))
	}
	if tokens[0].userID != store.user.ID {
		t.Errorf("token stored for %s, want %s", tokens[0].userID, store.user.ID)
	}
	if tokens[0].ttl != passwordreset.TokenTTL {
		t.Errorf("token TTL %v, want %v", tokens[0].ttl, passwordreset.TokenTTL)
	}
	digest := sha256.Sum256([]byte(raw))
	if string(tokens[0].hash) != string(digest[:]) {
		t.Error("the stored hash is not the SHA-256 of the emailed token")
	}
	if strings.Contains(string(tokens[0].hash), raw) {
		t.Error("the raw token reached the database")
	}
}

func TestRequestKeepsABasePathPrefix(t *testing.T) {
	t.Parallel()

	store := &fakeStore{user: knownUser("owner@example.com", "en")}
	svc, recorder := newService(t, store,
		passwordreset.Config{BaseURL: "https://example.com/hamlaneh/", Deliverable: true})

	if err := svc.Request(context.Background(), "ip", "owner@example.com"); err != nil {
		t.Fatalf("Request: %v", err)
	}
	sent := recorder.WaitFor(1, waitMail)
	if len(sent) != 1 {
		t.Fatalf("dispatched %d messages, want 1", len(sent))
	}
	if !strings.HasPrefix(sent[0].ResetURL, "https://example.com/hamlaneh/reset#token=") {
		t.Errorf("reset URL = %q, want the base path kept and the token in the fragment", sent[0].ResetURL)
	}
}

func TestRequestForAnUnknownAddressLooksIdentical(t *testing.T) {
	t.Parallel()

	store := &fakeStore{userErr: storage.ErrNotFound}
	svc, recorder := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	if err := svc.Request(context.Background(), "203.0.113.7", "nobody@example.com"); err != nil {
		t.Fatalf("Request for an unknown address = %v, want nil", err)
	}
	if got := store.tokens(); len(got) != 0 {
		t.Errorf("stored %d tokens for an unknown address, want 0", len(got))
	}
	if got := recorder.WaitFor(1, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("dispatched %d messages for an unknown address, want 0", len(got))
	}
	if got := store.lookups(); len(got) != 1 || got[0] != "nobody@example.com" {
		t.Errorf("looked up %v, want the presented address once", got)
	}
}

func TestRequestReportsStorageFailuresToTheCaller(t *testing.T) {
	t.Parallel()

	failure := errors.New("database is on fire")
	store := &fakeStore{user: knownUser("owner@example.com", "en"), createErr: failure}
	svc, _ := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	err := svc.Request(context.Background(), "ip", "owner@example.com")
	if !errors.Is(err, failure) {
		t.Fatalf("Request = %v, want the storage failure wrapped", err)
	}
}

func TestRequestDoesNothingWhenResetIsUnavailable(t *testing.T) {
	t.Parallel()

	store := &fakeStore{user: knownUser("owner@example.com", "en")}
	svc, recorder := newService(t, store, passwordreset.Config{})

	if err := svc.Request(context.Background(), "ip", "owner@example.com"); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if got := store.lookups(); len(got) != 0 {
		t.Errorf("looked up %v with reset unavailable, want nothing", got)
	}
	if got := recorder.WaitFor(1, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("dispatched %d messages with reset unavailable, want 0", len(got))
	}
}

func TestRequestRateLimitsPerAddress(t *testing.T) {
	t.Parallel()

	store := &fakeStore{userErr: storage.ErrNotFound}
	svc, _ := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	// An address that matches nothing still consumes budget; a limiter that
	// only counted real accounts would answer the question the endpoint
	// refuses to answer.
	const address = "nobody@example.com"
	var limited bool
	for attempt := range 20 {
		// A fresh IP each time, so only the address budget can trip.
		ip := "198.51.100." + string(rune('0'+attempt%10))
		if errors.Is(svc.Request(context.Background(), ip, address), passwordreset.ErrRateLimited) {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("repeated requests for one address were never rate limited")
	}

	// A different address still works from the same client.
	if err := svc.Request(context.Background(), "198.51.100.1", "someone.else@example.com"); err != nil {
		t.Errorf("a different address = %v, want nil", err)
	}
}

func TestRequestRateLimitsPerIP(t *testing.T) {
	t.Parallel()

	store := &fakeStore{userErr: storage.ErrNotFound}
	svc, _ := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	const ip = "203.0.113.9"
	var limited bool
	for attempt := range 60 {
		address := "victim" + string(rune('a'+attempt%26)) + "@example.com"
		if errors.Is(svc.Request(context.Background(), ip, address), passwordreset.ErrRateLimited) {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("a single client was never rate limited across many addresses")
	}

	if err := svc.Request(context.Background(), "203.0.113.10", "fresh@example.com"); err != nil {
		t.Errorf("a different client = %v, want nil", err)
	}
}

func TestCompleteAnswersIdenticallyForEveryBadToken(t *testing.T) {
	t.Parallel()

	for _, outcome := range []storage.ResetOutcome{
		storage.ResetOutcomeUnknown,
		storage.ResetOutcomeUsed,
		storage.ResetOutcomeExpired,
	} {
		t.Run(outcome.String(), func(t *testing.T) {
			t.Parallel()

			store := &fakeStore{outcome: outcome}
			svc, _ := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

			err := svc.Complete(context.Background(), "ip", "a-token-of-sufficient-length", "a new passphrase")
			if !errors.Is(err, passwordreset.ErrInvalidToken) {
				t.Errorf("Complete = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestCompleteAppliesTheReset(t *testing.T) {
	t.Parallel()

	owner := uuid.New()
	store := &fakeStore{outcome: storage.ResetOutcomeApplied, consumeUser: owner}
	svc, _ := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	const (
		token       = "an-emailed-token-value"
		newPassword = "a brand new passphrase"
	)
	if err := svc.Complete(context.Background(), "ip", token, newPassword); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	digest := sha256.Sum256([]byte(token))
	if string(store.consumedHash) != string(digest[:]) {
		t.Error("the token was not looked up by its SHA-256 digest")
	}
	if store.consumedPassword == newPassword {
		t.Fatal("the new password was stored in the clear")
	}
	ok, _, err := password.Verify(newPassword, store.consumedPassword)
	if err != nil {
		t.Fatalf("verify stored hash: %v", err)
	}
	if !ok {
		t.Error("the stored hash does not verify the new password")
	}
}

func TestCompleteRateLimitsPerClient(t *testing.T) {
	t.Parallel()

	store := &fakeStore{outcome: storage.ResetOutcomeUnknown}
	svc, _ := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	const ip = "203.0.113.11"
	var limited bool
	for range 40 {
		if errors.Is(svc.Complete(context.Background(), ip, "some-token-value-here", "a new passphrase"), passwordreset.ErrRateLimited) {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("repeated token submissions from one client were never rate limited")
	}
	if err := svc.Complete(context.Background(), "203.0.113.12", "some-token-value-here", "a new passphrase"); !errors.Is(err, passwordreset.ErrInvalidToken) {
		t.Errorf("a different client = %v, want ErrInvalidToken", err)
	}
}

func TestCompleteReportsStorageFailures(t *testing.T) {
	t.Parallel()

	failure := errors.New("database is on fire")
	store := &fakeStore{consumeErr: failure}
	svc, _ := newService(t, store, passwordreset.Config{BaseURL: baseURL, Deliverable: true})

	err := svc.Complete(context.Background(), "ip", "some-token-value-here", "a new passphrase")
	if !errors.Is(err, failure) {
		t.Errorf("Complete = %v, want the storage failure wrapped", err)
	}
	if errors.Is(err, passwordreset.ErrInvalidToken) {
		t.Error("an internal failure was reported as an invalid token")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	if _, err := passwordreset.New(nil, &mailer.Recorder{}, passwordreset.Config{}); err == nil {
		t.Error("New accepted a nil store")
	}
	if _, err := passwordreset.New(&fakeStore{}, nil, passwordreset.Config{}); err == nil {
		t.Error("New accepted a nil mailer")
	}
}

// TestFromEnv covers the constructor wiring calls at startup: it must fail
// loudly when mail is configured without a public base URL, and stay quiet
// and unavailable when nothing is configured at all.
func TestFromEnv(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	clearEnv := func(t *testing.T) {
		t.Helper()
		for _, name := range []string{
			mailer.EnvHost, mailer.EnvPort, mailer.EnvUsername, mailer.EnvPassword,
			mailer.EnvFrom, mailer.EnvFromName, mailer.EnvEncryption,
			passwordreset.EnvPublicURL,
		} {
			t.Setenv(name, "")
		}
	}

	t.Run("nothing configured yields an unavailable service", func(t *testing.T) {
		clearEnv(t)

		svc, err := passwordreset.FromEnv(&fakeStore{})
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		t.Cleanup(svc.Close)
		if svc.Available() {
			t.Error("a zero-config instance reports reset as available")
		}
	})

	t.Run("SMTP without a public URL fails at startup", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(mailer.EnvHost, "smtp.example.com")
		t.Setenv(mailer.EnvFrom, "hamlaneh@example.com")

		svc, err := passwordreset.FromEnv(&fakeStore{})
		if err == nil {
			svc.Close()
			t.Fatal("FromEnv accepted SMTP with no public base URL")
		}
		if !strings.Contains(err.Error(), passwordreset.EnvPublicURL) {
			t.Errorf("error %q does not name %s", err, passwordreset.EnvPublicURL)
		}
	})

	t.Run("SMTP with a public URL is available", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(mailer.EnvHost, "smtp.example.com")
		t.Setenv(mailer.EnvFrom, "hamlaneh@example.com")
		t.Setenv(passwordreset.EnvPublicURL, baseURL)

		svc, err := passwordreset.FromEnv(&fakeStore{})
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		t.Cleanup(svc.Close)
		if !svc.Available() {
			t.Error("a configured instance reports reset as unavailable")
		}
	})

	t.Run("a broken SMTP configuration fails at startup", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(mailer.EnvHost, "smtp.example.com") // no sender
		t.Setenv(passwordreset.EnvPublicURL, baseURL)

		if svc, err := passwordreset.FromEnv(&fakeStore{}); err == nil {
			svc.Close()
			t.Fatal("FromEnv accepted a host with no sender address")
		}
	})
}
