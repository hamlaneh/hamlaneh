package mailer_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/mailer"
)

// TestSMTPSendsPasswordReset drives the real net/smtp client against a
// local fake server and inspects the message that arrives.
func TestSMTPSendsPasswordReset(t *testing.T) {
	t.Parallel()

	const (
		recipient = "someone@example.com"
		link      = "https://chat.example.com/reset?token=R4W-T0K3N"
	)

	server := startFakeSMTP(t)
	transport := newTestSMTP(t, server, mailer.Config{
		From:     "hamlaneh@example.com",
		FromName: "Hamlaneh",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.SendPasswordReset(ctx, recipient, "en", link); err != nil {
		t.Fatalf("SendPasswordReset: %v", err)
	}

	got := server.messages()
	if len(got) != 1 {
		t.Fatalf("server received %d messages, want 1", len(got))
	}
	msg := got[0]
	if msg.from != "hamlaneh@example.com" {
		t.Errorf("MAIL FROM = %q, want the configured sender", msg.from)
	}
	if len(msg.to) != 1 || msg.to[0] != recipient {
		t.Errorf("RCPT TO = %v, want [%s]", msg.to, recipient)
	}

	headers, body := splitRFC5322(t, msg.data)
	containsAll(t, headers,
		`From: "Hamlaneh" <hamlaneh@example.com>`,
		"To: <"+recipient+">",
		"MIME-Version: 1.0",
		`Content-Type: text/plain; charset="utf-8"`,
		"Content-Transfer-Encoding: base64",
		"Auto-Submitted: auto-generated",
		"Subject: ",
		"Message-ID: <",
		"Date: ",
	)
	if strings.Contains(headers, link) {
		t.Error("the reset link leaked into a header")
	}
	containsAll(t, body, link, "reset the password", "30 minutes")
}

// TestSMTPSendsPersianMessage proves the Persian template survives the
// wire: the subject is an RFC 2047 encoded word and the body decodes back
// to UTF-8 Persian.
func TestSMTPSendsPersianMessage(t *testing.T) {
	t.Parallel()

	server := startFakeSMTP(t)
	transport := newTestSMTP(t, server, mailer.Config{From: "hamlaneh@example.com"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.SendPasswordReset(ctx, "someone@example.com", "fa", "https://x.test/reset?token=t"); err != nil {
		t.Fatalf("SendPasswordReset: %v", err)
	}

	got := server.messages()
	if len(got) != 1 {
		t.Fatalf("server received %d messages, want 1", len(got))
	}
	headers, body := splitRFC5322(t, got[0].data)
	if !strings.Contains(headers, "Subject: =?utf-8?q?") {
		t.Errorf("Persian subject is not an encoded word:\n%s", headers)
	}
	for _, line := range strings.Split(headers, "\r\n") {
		if !isASCII(line) {
			t.Errorf("header line is not ASCII: %q", line)
		}
	}
	containsAll(t, body, "بازنشانی", "پیوند")
}

func TestSMTPRejectsUnparseableRecipient(t *testing.T) {
	t.Parallel()

	server := startFakeSMTP(t)
	transport := newTestSMTP(t, server, mailer.Config{From: "hamlaneh@example.com"})

	err := transport.SendPasswordReset(context.Background(), "not an address", "en", "https://x.test/reset?token=t")
	if !errors.Is(err, mailer.ErrInvalidAddress) {
		t.Fatalf("SendPasswordReset to a malformed address = %v, want ErrInvalidAddress", err)
	}
	if got := server.messages(); len(got) != 0 {
		t.Errorf("server received %d messages, want none", len(got))
	}
}

// TestSMTPRejectsHeaderInjection pins the defense against a recipient
// carrying its own headers.
func TestSMTPRejectsHeaderInjection(t *testing.T) {
	t.Parallel()

	server := startFakeSMTP(t)
	transport := newTestSMTP(t, server, mailer.Config{From: "hamlaneh@example.com"})

	injected := "victim@example.com>\r\nBcc: attacker@example.com\r\nX: <x@x"
	if err := transport.SendPasswordReset(context.Background(), injected, "en", "u"); err == nil {
		t.Fatal("SendPasswordReset accepted a recipient carrying CRLF")
	}
	if got := server.messages(); len(got) != 0 {
		t.Errorf("server received %d messages, want none", len(got))
	}
}

// newTestSMTP points cfg at the fake server and turns encryption off — the
// fake speaks no TLS, and the encrypted paths are the SMTP server's own
// well-tested code, not ours.
func newTestSMTP(t *testing.T, server *fakeSMTP, cfg mailer.Config) *mailer.SMTP {
	t.Helper()

	host, portText, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split fake server address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse fake server port: %v", err)
	}

	cfg.Host = host
	cfg.Port = port
	cfg.Encryption = mailer.EncryptionNone

	transport, err := mailer.NewSMTP(cfg, testTTL)
	if err != nil {
		t.Fatalf("NewSMTP: %v", err)
	}
	return transport
}

// splitRFC5322 splits a captured message into its header block and its
// decoded body.
func splitRFC5322(t *testing.T, data string) (headers, body string) {
	t.Helper()

	headers, encoded, found := strings.Cut(data, "\r\n\r\n")
	if !found {
		t.Fatalf("message has no header/body separator:\n%s", data)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(encoded), "\r\n", ""))
	if err != nil {
		t.Fatalf("decode message body: %v", err)
	}
	return headers, string(decoded)
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// capturedMessage is one SMTP transaction the fake server saw.
type capturedMessage struct {
	from string
	to   []string
	data string
}

// fakeSMTP is a minimal SMTP server: enough of the dialogue for net/smtp to
// deliver a message, and nothing else.
type fakeSMTP struct {
	addr string

	mu   sync.Mutex
	msgs []capturedMessage
}

// startFakeSMTP listens on a loopback port and serves until the test ends.
func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeSMTP{addr: ln.Addr().String()}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			server.handle(conn)
		}
	}()

	t.Cleanup(func() {
		if closeErr := ln.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Errorf("close fake SMTP listener: %v", closeErr)
		}
		wg.Wait()
	})
	return server
}

func (f *fakeSMTP) messages() []capturedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedMessage(nil), f.msgs...)
}

func (f *fakeSMTP) record(msg capturedMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
}

// handle runs one SMTP conversation. Connections are served one at a time,
// which is all a test needs.
func (f *fakeSMTP) handle(conn net.Conn) {
	// A test fake has nowhere useful to report a close failure to; a broken
	// conversation shows up as a failed assertion instead.
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	reply := func(text string) bool {
		_, err := io.WriteString(conn, text+"\r\n")
		return err == nil
	}
	if !reply("220 fake ESMTP") {
		return
	}

	var msg capturedMessage
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimRight(line, "\r\n"))

		switch {
		case strings.HasPrefix(command, "EHLO"):
			if !reply("250-fake\r\n250 HELP") {
				return
			}
		case strings.HasPrefix(command, "HELO"):
			if !reply("250 fake") {
				return
			}
		case strings.HasPrefix(command, "MAIL FROM:"):
			msg.from = angleAddr(line)
			if !reply("250 OK") {
				return
			}
		case strings.HasPrefix(command, "RCPT TO:"):
			msg.to = append(msg.to, angleAddr(line))
			if !reply("250 OK") {
				return
			}
		case command == "DATA":
			if !reply("354 go ahead") {
				return
			}
			data, readErr := readDotStuffed(reader)
			if readErr != nil {
				return
			}
			msg.data = data
			f.record(msg)
			msg = capturedMessage{}
			if !reply("250 OK") {
				return
			}
		case command == "QUIT":
			reply("221 bye")
			return
		default:
			if !reply("250 OK") {
				return
			}
		}
	}
}

// readDotStuffed reads message data until the lone-dot terminator.
func readDotStuffed(reader *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" || line == ".\n" {
			return b.String(), nil
		}
		b.WriteString(line)
	}
}

// angleAddr extracts the address from a "MAIL FROM:<a@b>" style command.
func angleAddr(line string) string {
	_, rest, found := strings.Cut(line, "<")
	if !found {
		return ""
	}
	addr, _, _ := strings.Cut(rest, ">")
	return addr
}
