package mailer

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"
)

// templateFS holds the message bodies. They are the one place in the server
// that carries Persian text (CLAUDE.md language policy needs an amendment
// for it) and the one place a translator has to touch.
//
//go:embed templates
var templateFS embed.FS

// messages is every message template, parsed once. A broken template is a
// build-time defect, so failing to parse must crash the process rather than
// surface at the first password reset.
var messages = template.Must(template.ParseFS(templateFS, "templates/*.txt"))

// localeEN and localeFA are the two languages the UI ships in; anything
// else falls back to English.
const (
	localeEN = "en"
	localeFA = "fa"
)

// errMalformedTemplate reports a template that does not follow the
// subject-then-body convention.
var errMalformedTemplate = errors.New("mailer: malformed message template")

// message is one rendered mail: a subject line and a plain-text body.
type message struct {
	subject string
	body    string
}

// resetTemplateData is what the password-reset templates interpolate.
type resetTemplateData struct {
	ResetURL       string
	ExpiresMinutes int
}

// renderPasswordReset renders the reset mail in locale's language, falling
// back to English for any locale without a translation.
func renderPasswordReset(locale, resetURL string, ttl time.Duration) (message, error) {
	name := "password_reset." + templateLocale(locale) + ".txt"

	var buf bytes.Buffer
	data := resetTemplateData{ResetURL: resetURL, ExpiresMinutes: int(ttl.Minutes())}
	if err := messages.ExecuteTemplate(&buf, name, data); err != nil {
		return message{}, fmt.Errorf("mailer: render %s: %w", name, err)
	}
	return splitMessage(buf.String())
}

// templateLocale maps a user locale onto a template language.
func templateLocale(locale string) string {
	if strings.EqualFold(strings.TrimSpace(locale), localeFA) {
		return localeFA
	}
	return localeEN
}

// splitMessage splits a rendered template into its subject (the first line)
// and its body (everything after it). Keeping both in one file per locale
// means a translator edits one file and cannot leave the subject behind in
// the wrong language.
func splitMessage(rendered string) (message, error) {
	subject, body, found := strings.Cut(strings.TrimLeft(rendered, "\r\n"), "\n")
	if !found {
		return message{}, fmt.Errorf("%w: no body after the subject line", errMalformedTemplate)
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return message{}, fmt.Errorf("%w: empty subject line", errMalformedTemplate)
	}
	body = strings.TrimLeft(body, "\r\n")
	if body == "" {
		return message{}, fmt.Errorf("%w: empty body", errMalformedTemplate)
	}
	return message{subject: subject, body: body}, nil
}
