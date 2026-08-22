package mailer

import (
	"strings"
	"testing"
	"time"
)

// zwnj is the zero-width non-joiner Persian sets inside compound words —
// spelled as an escape rather than pasted in, so it stays visible to a
// reviewer and survives tooling that would normalize it away.
const zwnj = "\u200c"

func TestRenderPasswordReset(t *testing.T) {
	t.Parallel()

	const link = "https://chat.example.com/reset?token=Ry5TOKENy5R"

	tests := []struct {
		name        string
		locale      string
		wantSubject string
		wantInBody  string
	}{
		{
			name:        "english",
			locale:      "en",
			wantSubject: "Reset your Hamlaneh password",
			wantInBody:  "To choose a new password",
		},
		{
			name:        "persian",
			locale:      "fa",
			wantSubject: "بازنشانی رمز عبور هم" + zwnj + "لانه",
			wantInBody:  "این پیوند فقط",
		},
		{
			name:        "unknown locale falls back to english",
			locale:      "de",
			wantSubject: "Reset your Hamlaneh password",
			wantInBody:  "To choose a new password",
		},
		{
			name:        "empty locale falls back to english",
			locale:      "",
			wantSubject: "Reset your Hamlaneh password",
			wantInBody:  "To choose a new password",
		},
		{
			name:        "locale case is ignored",
			locale:      "FA",
			wantSubject: "بازنشانی رمز عبور هم" + zwnj + "لانه",
			wantInBody:  "این پیوند فقط",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := renderPasswordReset(tc.locale, link, 30*time.Minute)
			if err != nil {
				t.Fatalf("renderPasswordReset: %v", err)
			}
			if got.subject != tc.wantSubject {
				t.Errorf("subject = %q, want %q", got.subject, tc.wantSubject)
			}
			if !strings.Contains(got.body, tc.wantInBody) {
				t.Errorf("body does not contain %q:\n%s", tc.wantInBody, got.body)
			}
			if !strings.Contains(got.body, link) {
				t.Errorf("body does not carry the reset link:\n%s", got.body)
			}
			if !strings.Contains(got.body, "30") {
				t.Errorf("body does not state the 30-minute lifetime:\n%s", got.body)
			}
			if strings.Contains(got.body, "{{") {
				t.Errorf("body has an uninterpolated action:\n%s", got.body)
			}
		})
	}
}

func TestSplitMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rendered    string
		wantSubject string
		wantBody    string
		wantErr     bool
	}{
		{
			name:        "subject then body",
			rendered:    "Subject line\n\nFirst paragraph.\n",
			wantSubject: "Subject line",
			wantBody:    "First paragraph.\n",
		},
		{
			name:        "leading blank lines are ignored",
			rendered:    "\n\nSubject line\n\nBody.\n",
			wantSubject: "Subject line",
			wantBody:    "Body.\n",
		},
		{name: "no body", rendered: "Subject only", wantErr: true},
		{name: "empty subject", rendered: "   \n\nBody.\n", wantErr: true},
		{name: "empty body", rendered: "Subject\n\n\n", wantErr: true},
		{name: "empty input", rendered: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := splitMessage(tc.rendered)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitMessage = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitMessage: %v", err)
			}
			if got.subject != tc.wantSubject {
				t.Errorf("subject = %q, want %q", got.subject, tc.wantSubject)
			}
			if got.body != tc.wantBody {
				t.Errorf("body = %q, want %q", got.body, tc.wantBody)
			}
		})
	}
}

// TestEveryTemplateIsWellFormed keeps a translator from shipping a file
// that renders but has no subject or no body.
func TestEveryTemplateIsWellFormed(t *testing.T) {
	t.Parallel()

	for _, locale := range []string{localeEN, localeFA} {
		got, err := renderPasswordReset(locale, "https://x.test/reset?token=t", time.Minute)
		if err != nil {
			t.Errorf("locale %q: %v", locale, err)
			continue
		}
		if got.subject == "" || got.body == "" {
			t.Errorf("locale %q rendered subject %q body %q", locale, got.subject, got.body)
		}
	}
}
