package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP delivers mail through an SMTP submission server using the standard
// library's net/smtp. It is safe for concurrent use: every send opens and
// closes its own connection, which is the right trade for the handful of
// messages a self-hosted instance sends.
//
// Callers should wrap it in a Queue — nothing in an HTTP handler may wait
// for a remote SMTP server.
type SMTP struct {
	cfg          Config
	resetLinkTTL time.Duration
}

var _ Mailer = (*SMTP)(nil)

// NewSMTP returns an SMTP mailer for cfg, filling in the defaults an
// incomplete configuration implies and refusing one that cannot work.
// resetLinkTTL is wording only: it is how long the message says the link
// lasts, and must match internal/passwordreset.TokenTTL, which enforces it.
func NewSMTP(cfg Config, resetLinkTTL time.Duration) (*SMTP, error) {
	normalized, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	if !normalized.Configured() {
		return nil, fmt.Errorf("%w: %s is empty", ErrNotConfigured, EnvHost)
	}
	if normalized.Encryption == EncryptionNone {
		slog.Warn("SMTP encryption is disabled; mail leaves this server in the clear",
			"host", normalized.Host, "port", normalized.Port)
	}
	return &SMTP{cfg: normalized, resetLinkTTL: resetLinkTTL}, nil
}

// SendPasswordReset renders the reset mail in the recipient's language and
// delivers it. Neither the link nor the token ever reaches a log line.
func (m *SMTP) SendPasswordReset(ctx context.Context, to, locale, resetURL string) error {
	msg, err := renderPasswordReset(locale, resetURL, m.resetLinkTTL)
	if err != nil {
		return err
	}
	raw, err := m.compose(to, msg)
	if err != nil {
		return err
	}
	return m.send(ctx, to, raw)
}

// compose builds the RFC 5322 message. The body is base64-encoded UTF-8 and
// the subject is an RFC 2047 encoded word, so Persian survives every hop.
func (m *SMTP) compose(to string, msg message) ([]byte, error) {
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAddress, err)
	}
	from := mail.Address{Name: m.cfg.FromName, Address: m.cfg.From}

	id, err := messageID(m.cfg.From)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	writeHeader(&b, "From", from.String())
	writeHeader(&b, "To", recipient.String())
	writeHeader(&b, "Subject", mime.QEncoding.Encode("utf-8", msg.subject))
	writeHeader(&b, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&b, "Message-ID", id)
	writeHeader(&b, "MIME-Version", "1.0")
	writeHeader(&b, "Content-Type", `text/plain; charset="utf-8"`)
	writeHeader(&b, "Content-Transfer-Encoding", "base64")
	// Tells mailing lists and vacation responders not to reply (RFC 3834).
	writeHeader(&b, "Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(msg.body))
	return []byte(b.String()), nil
}

// writeHeader appends one header field. Values reaching here are either
// constants or already encoded (encoded words and parsed addresses cannot
// contain CR or LF), so no header-injection vector survives.
func writeHeader(b *strings.Builder, name, value string) {
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\r\n")
}

// messageID returns a globally unique Message-ID whose domain is the
// sender's, per RFC 5322.
func messageID(from string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mailer: generate message id: %w", err)
	}
	domain := from
	if _, after, found := strings.Cut(from, "@"); found {
		domain = after
	}
	return "<" + hex.EncodeToString(buf) + "@" + domain + ">", nil
}

// wrapBase64 encodes body and folds it into the 76-character lines RFC 2045
// requires, with CRLF endings.
func wrapBase64(body string) string {
	const lineLen = 76

	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	var b strings.Builder
	for len(encoded) > lineLen {
		b.WriteString(encoded[:lineLen])
		b.WriteString("\r\n")
		encoded = encoded[lineLen:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")
	return b.String()
}

// send runs one SMTP conversation: connect, protect, then envelope and data.
func (m *SMTP) send(ctx context.Context, to string, raw []byte) error {
	client, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer closeClient(client)

	if handshakeErr := m.handshake(client); handshakeErr != nil {
		return handshakeErr
	}
	return deliver(client, m.cfg.From, to, raw)
}

// connect dials the server and wraps the connection in an SMTP client,
// applying implicit TLS when that is the configured protection.
func (m *SMTP) connect(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mailer: dial %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if deadlineErr := conn.SetDeadline(deadline); deadlineErr != nil {
			return nil, errors.Join(
				fmt.Errorf("mailer: set connection deadline: %w", deadlineErr), conn.Close())
		}
	}
	if m.cfg.Encryption == EncryptionTLS {
		conn = tls.Client(conn, m.tlsConfig())
	}

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("mailer: open SMTP session: %w", err), conn.Close())
	}
	return client, nil
}

// handshake upgrades the connection with STARTTLS when that is the
// configured protection, then authenticates if credentials are set.
func (m *SMTP) handshake(client *smtp.Client) error {
	if m.cfg.Encryption == EncryptionStartTLS {
		if err := client.StartTLS(m.tlsConfig()); err != nil {
			return fmt.Errorf("mailer: STARTTLS: %w", err)
		}
	}
	if m.cfg.Username == "" {
		return nil
	}
	// PlainAuth refuses to send credentials over an unprotected connection
	// unless the server is local — a stdlib guarantee worth relying on.
	auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("mailer: authenticate: %w", err)
	}
	return nil
}

// deliver sends one envelope and its message body.
func deliver(client *smtp.Client, from, to string, raw []byte) error {
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mailer: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("mailer: RCPT TO: %w", err)
	}

	data, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	if _, writeErr := data.Write(raw); writeErr != nil {
		return errors.Join(fmt.Errorf("mailer: write message: %w", writeErr), data.Close())
	}
	if closeErr := data.Close(); closeErr != nil {
		return fmt.Errorf("mailer: finish message: %w", closeErr)
	}
	if quitErr := client.Quit(); quitErr != nil {
		return fmt.Errorf("mailer: QUIT: %w", quitErr)
	}
	return nil
}

// tlsConfig is the TLS policy for both STARTTLS and implicit TLS: verified
// certificates, TLS 1.2 or better. There is deliberately no switch to turn
// verification off.
func (m *SMTP) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}
}

// closeClient releases the connection. A successful Quit already closed it,
// so that one expected error is not worth a log line.
func closeClient(client *smtp.Client) {
	if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Debug("mailer: close SMTP connection", "error", err)
	}
}
