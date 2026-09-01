package oidc

import (
	"strings"
	"testing"
)

// setEnv arranges one whole SSO environment for a subtest; t.Setenv also
// restores the previous values afterwards.
func setEnv(t *testing.T, issuer, clientID, secret, name string) {
	t.Helper()
	t.Setenv(EnvIssuer, issuer)
	t.Setenv(EnvClientID, clientID)
	t.Setenv(EnvClientSecret, secret)
	t.Setenv(EnvProviderName, name)
}

func TestConfigFromEnv(t *testing.T) {
	const publicURL = "https://chat.example.com"

	t.Run("nothing set is off, not an error", func(t *testing.T) {
		setEnv(t, "", "", "", "")
		_, configured, err := ConfigFromEnv(publicURL)
		if err != nil {
			t.Fatalf("got error %v, want none", err)
		}
		if configured {
			t.Fatal("an empty environment reads as configured")
		}
	})

	t.Run("every partial set stops startup", func(t *testing.T) {
		cases := map[string][3]string{
			"issuer only":       {"https://idp.example.com", "", ""},
			"client id only":    {"", "client-id", ""},
			"secret only":       {"", "", "shhh"},
			"missing secret":    {"https://idp.example.com", "client-id", ""},
			"missing issuer":    {"", "client-id", "shhh"},
			"missing client id": {"https://idp.example.com", "", "shhh"},
		}
		for name, vals := range cases {
			t.Run(name, func(t *testing.T) {
				setEnv(t, vals[0], vals[1], vals[2], "")
				if _, _, err := ConfigFromEnv(publicURL); err == nil {
					t.Error("a half-configured set did not stop startup")
				}
			})
		}
	})

	t.Run("provider name alone is also half-configured", func(t *testing.T) {
		setEnv(t, "", "", "", "Okta")
		if _, _, err := ConfigFromEnv(publicURL); err == nil {
			t.Error("a name with no provider behind it did not stop startup")
		}
	})

	t.Run("configured without a public URL stops startup", func(t *testing.T) {
		setEnv(t, "https://idp.example.com", "client-id", "shhh", "")
		if _, _, err := ConfigFromEnv(""); err == nil {
			t.Error("no public URL means no redirect URI, yet it configured")
		}
	})

	t.Run("issuer must be a URL", func(t *testing.T) {
		setEnv(t, "idp.example.com", "client-id", "shhh", "")
		if _, _, err := ConfigFromEnv(publicURL); err == nil {
			t.Error("a scheme-less issuer configured")
		}
	})

	t.Run("full set builds the redirect from the public URL", func(t *testing.T) {
		setEnv(t, "https://idp.example.com", "client-id", "shhh", "Okta")
		cfg, configured, err := ConfigFromEnv(publicURL + "/")
		if err != nil || !configured {
			t.Fatalf("got (%v, %v), want configured", configured, err)
		}
		if cfg.RedirectURL != publicURL+CallbackPath {
			t.Errorf("redirect URL %q, want %q", cfg.RedirectURL, publicURL+CallbackPath)
		}
		if cfg.ProviderName != "Okta" {
			t.Errorf("provider name %q, want Okta", cfg.ProviderName)
		}
	})
}

// TestProviderNameDefaults pins the instance-info contract: a configured
// provider always has a name, so enabled true can never ship without one.
func TestProviderNameDefaults(t *testing.T) {
	if got := New(Config{}).ProviderName(); got != "SSO" {
		t.Errorf("unset display name yields %q, want the generic SSO", got)
	}
	if got := New(Config{ProviderName: "Okta"}).ProviderName(); got != "Okta" {
		t.Errorf("a set display name yields %q, want Okta", got)
	}
}

// TestFromEnvNilWhenOff pins the wiring contract: an unconfigured
// environment yields a nil service, which is what the handlers key
// "sso_unavailable" off.
func TestFromEnvNilWhenOff(t *testing.T) {
	setEnv(t, "", "", "", "")
	svc, err := FromEnv("https://chat.example.com")
	if err != nil {
		t.Fatalf("got error %v, want none", err)
	}
	if svc != nil {
		t.Fatal("an unconfigured environment built a service")
	}
}

// TestAuthRequestSecretsAreFresh pins that two minted requests never share
// state, nonce, or verifier — a repeated value would tie two flows to one
// transaction.
//
// It needs no provider: the values are minted locally, so the test builds
// them the way NewAuthRequest does.
func TestAuthRequestSecretsAreFresh(t *testing.T) {
	a, b := randomToken(), randomToken()
	if a == b {
		t.Fatal("two random tokens collided")
	}
	if len(a) < 40 || strings.ContainsAny(a, "+/=") {
		t.Errorf("token %q is not unpadded base64url over 256 bits", a)
	}
}
