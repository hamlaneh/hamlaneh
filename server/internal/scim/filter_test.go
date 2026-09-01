package scim

import (
	"strings"
	"testing"
)

// The filter parser's whole job is to refuse what it does not understand.
// The dangerous failure is not a rejected filter — it is an accepted one
// that quietly matched nothing in particular, because a lookup for one
// person would then answer with the directory.

// ptr is the table's way of saying "this field must be set, to this" as
// distinct from "this field must be nil". An empty string is a real filter
// value and must not read as an absent one.
func ptr(s string) *string { return &s }

func TestParseFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		raw            string
		wantUserName   *string
		wantExternalID *string
		wantErr        bool
	}{
		{name: "empty is no filter", raw: ""},
		{name: "whitespace is no filter", raw: "   "},
		{name: "okta userName lookup", raw: `userName eq "amir@example.com"`, wantUserName: ptr("amir@example.com")},
		{name: "entra externalId lookup", raw: `externalId eq "00u1abc"`, wantExternalID: ptr("00u1abc")},
		{name: "attribute names are case-insensitive", raw: `USERNAME eq "a@b"`, wantUserName: ptr("a@b")},
		{name: "the operator is case-insensitive", raw: `userName EQ "a@b"`, wantUserName: ptr("a@b")},
		{name: "the schema urn may qualify the attribute",
			raw: `urn:ietf:params:scim:schemas:core:2.0:User:userName eq "a@b"`, wantUserName: ptr("a@b")},
		{name: "extra spacing is tolerated", raw: `  userName   eq   "a@b"  `, wantUserName: ptr("a@b")},
		{name: "an escaped quote survives", raw: `userName eq "a\"b"`, wantUserName: ptr(`a"b`)},
		{name: "an empty value is a real filter", raw: `userName eq ""`, wantUserName: ptr("")},

		{name: "an unmapped attribute is refused", raw: `emails.value eq "a@b"`, wantErr: true},
		{name: "is_admin is not a filterable attribute", raw: `is_admin eq "true"`, wantErr: true},
		{name: "roles are refused like anything else unmapped", raw: `roles eq "admin"`, wantErr: true},
		{name: "an unsupported operator is refused", raw: `userName co "amir"`, wantErr: true},
		{name: "a compound expression is refused", raw: `userName eq "a" and active eq true`, wantErr: true},
		{name: "an unquoted value is refused", raw: `userName eq amir`, wantErr: true},
		{name: "a bare attribute is refused", raw: `userName`, wantErr: true},
		{name: "a present-operator filter is refused", raw: `userName pr`, wantErr: true},
		{name: "an absurdly long filter is refused", raw: `userName eq "` + strings.Repeat("a", 600) + `"`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseFilter(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseFilter(%q) = %+v, want an error", tt.raw, got)
				}
				if got.UserName != nil || got.ExternalID != nil {
					t.Errorf("a refused filter came back with fields set: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFilter(%q): %v", tt.raw, err)
			}
			assertField(t, "userName", got.UserName, tt.wantUserName)
			assertField(t, "externalId", got.ExternalID, tt.wantExternalID)
		})
	}
}

// assertField compares one optional filter field by value, because comparing
// the pointers would pass for two filters that select different people.
func assertField(t *testing.T, name string, got, want *string) {
	t.Helper()

	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %q, want unset", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s is unset, want %q", name, *want)
	case want != nil && *got != *want:
		t.Errorf("%s = %q, want %q", name, *got, *want)
	}
}

// TestParseFilterNeverMatchesEverything is the security property behind the
// refusals: a filter that parsed must select on exactly one attribute.
// Anything that came back with neither field set would be read downstream as
// "no filter", which answers a lookup for one person with every account.
func TestParseFilterNeverMatchesEverything(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`userName eq "a"`, `externalId eq "b"`, `USERNAME eq ""`,
	} {
		got, err := parseFilter(raw)
		if err != nil {
			t.Fatalf("parseFilter(%q): %v", raw, err)
		}
		if (got.UserName == nil) == (got.ExternalID == nil) {
			t.Errorf("parseFilter(%q) = %+v, want exactly one field set", raw, got)
		}
	}
}

// FuzzParseFilter is the input-handling guarantee (CLAUDE.md): a filter
// arrives in a query string from an external system, so no value of it may
// panic — and none may parse into the filter that matches everything, which
// is the one wrong answer that would not look like a failure.
func FuzzParseFilter(f *testing.F) {
	for _, seed := range []string{
		"", `userName eq "a@b"`, `externalId eq "x"`, `userName eq`, `userName`,
		`userName eq "a" and externalId eq "b"`, `userName EQ "A"`,
		`emails[type eq "work"].value eq "a"`, `((`,
		"userName eq \"a\nb\"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := parseFilter(raw)
		if err != nil {
			if got.UserName != nil || got.ExternalID != nil {
				t.Errorf("parseFilter(%q) failed but returned %+v", raw, got)
			}
			return
		}
		if strings.TrimSpace(raw) == "" {
			return // the empty filter is the one that legitimately matches all
		}
		if got.UserName == nil && got.ExternalID == nil {
			t.Errorf("parseFilter(%q) accepted a non-empty filter that selects nothing", raw)
		}
		if got.UserName != nil && got.ExternalID != nil {
			t.Errorf("parseFilter(%q) set both fields: %+v", raw, got)
		}
	})
}
