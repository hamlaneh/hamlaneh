package mailer

import (
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables that configure the SMTP transport. Setting the host
// is what turns mail on; everything else refines it. A zero-config install
// sets none of them and gets the Null mailer.
const (
	// EnvHost is the SMTP server hostname. Empty means no mail transport.
	EnvHost = "HAMLANEH_SMTP_HOST"
	// EnvPort overrides the port implied by the encryption mode.
	EnvPort = "HAMLANEH_SMTP_PORT"
	// EnvUsername is the SMTP submission username (optional).
	EnvUsername = "HAMLANEH_SMTP_USERNAME"
	// EnvPassword is the SMTP submission password (optional).
	EnvPassword = "HAMLANEH_SMTP_PASSWORD" // #nosec G101 -- the environment variable's NAME; its value is never in the repo
	// EnvFrom is the envelope and header From address; required with a host.
	EnvFrom = "HAMLANEH_SMTP_FROM"
	// EnvFromName is the optional display name shown beside EnvFrom.
	EnvFromName = "HAMLANEH_SMTP_FROM_NAME"
	// EnvEncryption selects starttls (default), tls, or none.
	EnvEncryption = "HAMLANEH_SMTP_ENCRYPTION"
)

// Encryption selects how the SMTP connection is protected.
type Encryption string

// Supported connection protections.
const (
	// EncryptionStartTLS dials plaintext and upgrades with STARTTLS. It is
	// the default because submission (port 587) expects it.
	EncryptionStartTLS Encryption = "starttls"
	// EncryptionTLS dials TLS directly (implicit TLS, port 465).
	EncryptionTLS Encryption = "tls"
	// EncryptionNone sends over an unprotected connection. It exists for a
	// relay reachable only across a private container network, must be
	// chosen explicitly, and nothing ever defaults to it.
	EncryptionNone Encryption = "none"
)

// Default ports per encryption mode, and the network budgets one message
// gets.
const (
	defaultSubmissionPort  = 587
	defaultImplicitTLSPort = 465
	defaultPlainPort       = 25

	dialTimeout = 10 * time.Second
	// SendTimeout bounds one whole SMTP conversation.
	SendTimeout = 30 * time.Second
)

// Config describes the SMTP transport. The zero value means "no mail
// transport configured", which is a valid, supported deployment.
type Config struct {
	// Host is the SMTP server. Empty disables mail entirely.
	Host string
	// Port defaults to the submission port for the encryption mode.
	Port int
	// Username and Password authenticate submission; both or neither.
	Username string
	Password string
	// From is the envelope sender and the From header address.
	From string
	// FromName is the optional display name shown beside From.
	FromName string
	// Encryption defaults to EncryptionStartTLS.
	Encryption Encryption
}

// Configured reports whether a mail transport is configured at all. It is
// the value GET /api/v1/instance exposes as password_reset_available: a
// "Forgot password?" link that goes nowhere is dishonest.
func (c Config) Configured() bool { return c.Host != "" }

// ConfigFromEnv reads the SMTP configuration from the process environment.
// An unset host yields the zero Config, for which New returns the Null
// mailer. Any other combination is validated here, so a half-configured
// transport fails at startup instead of at the first password reset.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Host:       strings.TrimSpace(os.Getenv(EnvHost)),
		Username:   os.Getenv(EnvUsername),
		Password:   os.Getenv(EnvPassword),
		From:       strings.TrimSpace(os.Getenv(EnvFrom)),
		FromName:   strings.TrimSpace(os.Getenv(EnvFromName)),
		Encryption: Encryption(strings.ToLower(strings.TrimSpace(os.Getenv(EnvEncryption)))),
	}
	if !cfg.Configured() {
		return Config{}, nil
	}

	if raw := strings.TrimSpace(os.Getenv(EnvPort)); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%w: %s must be a number, got %q", ErrNotConfigured, EnvPort, raw)
		}
		cfg.Port = port
	}

	normalized, err := cfg.normalize()
	if err != nil {
		return Config{}, err
	}
	return normalized, nil
}

// normalize fills the defaults a partially specified Config implies and
// rejects one that cannot work.
func (c Config) normalize() (Config, error) {
	switch c.Encryption {
	case "":
		c.Encryption = EncryptionStartTLS
	case EncryptionStartTLS, EncryptionTLS, EncryptionNone:
	default:
		return Config{}, fmt.Errorf("%w: %s must be one of %q, %q, %q, got %q",
			ErrNotConfigured, EnvEncryption,
			EncryptionStartTLS, EncryptionTLS, EncryptionNone, c.Encryption)
	}

	if c.Port == 0 {
		c.Port = defaultPortFor(c.Encryption)
	}
	if c.Port < 1 || c.Port > 65535 {
		return Config{}, fmt.Errorf("%w: %s must be 1 to 65535, got %d", ErrNotConfigured, EnvPort, c.Port)
	}

	if c.From == "" {
		return Config{}, fmt.Errorf("%w: %s is set, so %s must be too", ErrNotConfigured, EnvHost, EnvFrom)
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return Config{}, fmt.Errorf("%w: %s is not a valid address: %w", ErrNotConfigured, EnvFrom, err)
	}
	if (c.Username == "") != (c.Password == "") {
		return Config{}, fmt.Errorf("%w: set both %s and %s, or neither", ErrNotConfigured, EnvUsername, EnvPassword)
	}
	return c, nil
}

// defaultPortFor returns the conventional port for an encryption mode.
func defaultPortFor(enc Encryption) int {
	switch enc {
	case EncryptionTLS:
		return defaultImplicitTLSPort
	case EncryptionNone:
		return defaultPlainPort
	case EncryptionStartTLS:
		return defaultSubmissionPort
	default:
		return defaultSubmissionPort
	}
}

// New returns the Mailer for cfg: an SMTP transport when one is configured,
// the Null mailer otherwise. resetLinkTTL is wording only — how long the
// message says the link lasts — and must match the policy that enforces it
// (internal/passwordreset.TokenTTL).
func New(cfg Config, resetLinkTTL time.Duration) (Mailer, error) {
	if !cfg.Configured() {
		return Null{}, nil
	}
	return NewSMTP(cfg, resetLinkTTL)
}
