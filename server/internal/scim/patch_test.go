package scim

import (
	"encoding/json"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The PatchOp decoder, tested against the shapes the two providers actually
// send. Their forms differ — Okta patches with no path and an object value,
// Entra with a path, a capitalised op and sometimes a quoted boolean — and
// that difference is the real interop risk of this surface.
//
// The refusals matter as much as the acceptances, and one of them matters
// more than the rest: no shape of patch may reach is_admin.

// baseWrite is an ordinary directory-managed account as a patch finds it.
func baseWrite() userWrite {
	return userWrite{
		attrs: storage.ScimUserAttributes{
			ScimUserName: "amir@example.com",
			ExternalID:   ptr("00u1abc"),
			Email:        ptr("amir@example.com"),
			DisplayName:  "Amir Dezyani",
		},
		active: true,
	}
}

// sameWrite compares two write states by value. Go's == would compare the
// *string fields by address, so two identical accounts built separately
// would never be equal.
func sameWrite(a, b userWrite) bool {
	return a.active == b.active && sameAttributes(a.attrs, b.attrs)
}

// patchOf parses a body the way the handler does, so the tests exercise the
// decoder through the same door a provider comes in by.
func patchOf(t *testing.T, body string) patchRequest {
	t.Helper()

	var req patchRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("parse patch body: %v", err)
	}
	return req
}

func TestApplyPatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		check  func(t *testing.T, got userWrite)
		reason string // non-empty means the patch must be refused with this scimType
	}{
		{
			name: "okta deactivation: no path, object value",
			body: `{"Operations":[{"op":"replace","value":{"active":false}}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.active {
					t.Error("account is still active")
				}
			},
		},
		{
			name: "entra deactivation: path, capitalised op, quoted boolean",
			body: `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.active {
					t.Error("account is still active")
				}
			},
		},
		{
			name: "entra reactivation with a real boolean",
			body: `{"Operations":[{"op":"replace","path":"active","value":true}]}`,
			check: func(t *testing.T, got userWrite) {
				if !got.active {
					t.Error("account was not reactivated")
				}
			},
		},
		{
			name: "entra display name through name.formatted",
			body: `{"Operations":[{"op":"replace","path":"name.formatted","value":"Amir D"}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.DisplayName != "Amir D" {
					t.Errorf("displayName = %q", got.attrs.DisplayName)
				}
			},
		},
		{
			name: "entra email through a filtered multi-valued path",
			body: `{"Operations":[{"op":"replace","path":"emails[type eq \"work\"].value","value":"new@example.com"}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.Email == nil || *got.attrs.Email != "new@example.com" {
					t.Errorf("email = %v", got.attrs.Email)
				}
			},
		},
		{
			name: "okta email as the whole multi-valued array",
			body: `{"Operations":[{"op":"replace","path":"emails","value":[` +
				`{"value":"secondary@example.com","primary":false},` +
				`{"value":"primary@example.com","primary":true}]}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.Email == nil || *got.attrs.Email != "primary@example.com" {
					t.Errorf("email = %v, want the primary entry", got.attrs.Email)
				}
			},
		},
		{
			name: "the schema urn may qualify the path",
			body: `{"Operations":[{"op":"replace","path":"urn:ietf:params:scim:schemas:core:2.0:User:userName","value":"new@example.com"}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.ScimUserName != "new@example.com" {
					t.Errorf("userName = %q", got.attrs.ScimUserName)
				}
			},
		},
		{
			name: "removing an email clears it",
			body: `{"Operations":[{"op":"remove","path":"emails"}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.Email != nil {
					t.Errorf("email = %v, want cleared", *got.attrs.Email)
				}
			},
		},
		{
			name: "removing externalId gives the account back to nobody",
			body: `{"Operations":[{"op":"remove","path":"externalId"}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.ExternalID != nil {
					t.Errorf("externalId = %v, want cleared", *got.attrs.ExternalID)
				}
			},
		},
		{
			name: "setting an attribute to empty clears it rather than storing a blank",
			body: `{"Operations":[{"op":"replace","path":"externalId","value":""}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.ExternalID != nil {
					t.Errorf("externalId = %q, want nil so the unique column stays free", *got.attrs.ExternalID)
				}
			},
		},
		{
			name: "several operations apply in order",
			body: `{"Operations":[` +
				`{"op":"replace","path":"displayName","value":"First"},` +
				`{"op":"replace","path":"displayName","value":"Second"},` +
				`{"op":"replace","path":"active","value":false}]}`,
			check: func(t *testing.T, got userWrite) {
				if got.attrs.DisplayName != "Second" || got.active {
					t.Errorf("got %+v", got.attrs)
				}
			},
		},
		{
			name: "a pushed password is ignored, not refused",
			body: `{"Operations":[{"op":"replace","path":"password","value":"hunter2"}]}`,
			check: func(t *testing.T, got userWrite) {
				if !sameAttributes(got.attrs, baseWrite().attrs) {
					t.Errorf("a password patch changed something: %+v", got.attrs)
				}
			},
		},

		// Refusals. The first three are the ones that keep a sync token from
		// being worth more than the accounts it provisions.
		{name: "is_admin is not a path", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"replace","path":"is_admin","value":true}]}`},
		{name: "isAdmin is not a path either", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"replace","path":"isAdmin","value":true}]}`},
		{name: "roles are not a path", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"add","path":"roles","value":[{"value":"admin"}]}]}`},
		{name: "groups are not a path", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"add","path":"groups","value":[{"value":"admins"}]}]}`},
		{name: "an unknown attribute is refused", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"replace","path":"nickName","value":"amir"}]}`},
		{name: "an unknown sub-attribute of a mapped one is refused", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"replace","path":"name.givenName","value":"Amir"}]}`},
		{name: "remove needs a path", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"remove"}]}`},
		{name: "an unknown op is refused", reason: typeInvalidValue,
			body: `{"Operations":[{"op":"increment","path":"active","value":1}]}`},
		{name: "a non-boolean active is refused", reason: typeInvalidValue,
			body: `{"Operations":[{"op":"replace","path":"active","value":"maybe"}]}`},
		{name: "userName cannot be emptied", reason: typeInvalidValue,
			body: `{"Operations":[{"op":"replace","path":"userName","value":""}]}`},
		{name: "userName cannot be removed", reason: typeInvalidValue,
			body: `{"Operations":[{"op":"remove","path":"userName"}]}`},
		{name: "active cannot be removed", reason: typeInvalidValue,
			body: `{"Operations":[{"op":"remove","path":"active"}]}`},
		{name: "an empty operation list is refused", reason: typeInvalidValue,
			body: `{"Operations":[]}`},
		{name: "a pathless operation needs an object", reason: typeInvalidValue,
			body: `{"Operations":[{"op":"replace","value":"amir"}]}`},
		{name: "a pathless operation is still checked per attribute", reason: typeInvalidPath,
			body: `{"Operations":[{"op":"replace","value":{"is_admin":true}}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, failure := applyPatch(baseWrite(), patchOf(t, tt.body))
			if tt.reason != "" {
				if failure == nil {
					t.Fatalf("patch was accepted, want %s; result %+v", tt.reason, got)
				}
				if failure.scimType != tt.reason {
					t.Errorf("scimType = %q, want %q (%s)", failure.scimType, tt.reason, failure.detail)
				}
				if !sameWrite(got, baseWrite()) {
					t.Errorf("a refused patch changed the account: %+v", got)
				}
				return
			}
			if failure != nil {
				t.Fatalf("patch refused: %s: %s", failure.scimType, failure.detail)
			}
			tt.check(t, got)
		})
	}
}

// TestApplyPatchLeavesTheAccountAloneOnFailure is the atomicity property: a
// patch whose third operation is bad must not leave the first two applied.
// The handler writes what applyPatch returns, so a partial result here would
// be a partial write in production.
func TestApplyPatchLeavesTheAccountAloneOnFailure(t *testing.T) {
	t.Parallel()

	body := `{"Operations":[` +
		`{"op":"replace","path":"displayName","value":"Changed"},` +
		`{"op":"replace","path":"active","value":false},` +
		`{"op":"replace","path":"is_admin","value":true}]}`

	got, failure := applyPatch(baseWrite(), patchOf(t, body))
	if failure == nil {
		t.Fatal("the is_admin operation was accepted")
	}
	if !sameWrite(got, baseWrite()) {
		t.Errorf("the earlier operations survived a refused patch: %+v", got)
	}
}

// TestStripValueFilters pins the path normaliser on its own, including the
// case a hand-written scanner gets wrong: a closing bracket inside the
// filter's quoted literal.
func TestStripValueFilters(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"emails":                       "emails",
		`emails[type eq "work"]`:       "emails",
		`emails[type eq "work"].value`: "emails.value",
		`emails[value eq "a]b"].value`: "emails.value",
		`emails[`:                      "emails",
		"":                             "",
	}
	for in, want := range tests {
		if got := stripValueFilters(in); got != want {
			t.Errorf("stripValueFilters(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzApplyPatch is the input-handling guarantee (CLAUDE.md): a PatchOp body
// is untrusted JSON from an external system. No value of it may panic, and —
// the property that matters — none may reach an attribute outside the
// mapping. The account's own id, role and activation-by-accident are checked
// by construction: userWrite has no field for a role, so the assertion here
// is that a refused patch changes nothing and an accepted one leaves a
// well-formed attribute set.
func FuzzApplyPatch(f *testing.F) {
	for _, seed := range []string{
		`{"Operations":[{"op":"replace","value":{"active":false}}]}`,
		`{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`,
		`{"Operations":[{"op":"remove","path":"emails"}]}`,
		`{"Operations":[{"op":"add","path":"emails[type eq \"work\"].value","value":"a@b"}]}`,
		`{"Operations":[]}`,
		`{"Operations":[{"op":"replace","path":"is_admin","value":true}]}`,
		`{}`,
		`{"Operations":[{"op":"replace","path":"","value":null}]}`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		var req patchRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			return // the handler answers 400 before applyPatch is ever called
		}

		before := baseWrite()
		got, failure := applyPatch(before, req)
		if failure != nil {
			if !sameWrite(got, before) {
				t.Errorf("a refused patch changed the account: %+v", got)
			}
			return
		}
		if got.attrs.ScimUserName == "" {
			t.Errorf("an accepted patch left userName empty: %q", body)
		}
	})
}
