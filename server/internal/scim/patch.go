package scim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// The RFC 7644 PatchOp decoder (scim.md §2).
//
// This is where the two providers differ most, and the difference is the
// real interop risk of the whole surface. Okta sends a PATCH with no path
// and an object value — {"op":"replace","value":{"active":false}}. Entra
// sends a path and a scalar, capitalises the op, and has been observed
// sending the boolean as the string "False". Both are legal-ish readings of
// the specification and both have to work, so this decoder is deliberately
// lenient about SHAPE and strict about TARGET: any path it does not
// recognise is 400 invalidPath rather than a silently dropped operation.
//
// Strict about target is the security half. is_admin has no path here, and
// no shape of request produces one, which is what keeps a compromised sync
// token from minting an administrator (§2). "roles" and "groups" are real
// SCIM attributes this server maps onto nothing, so they are refused by name
// like any other unknown path rather than quietly ignored.

// patchRequest is the PatchOp envelope. Go matches JSON field names
// case-insensitively, so "Operations" and "operations" both land here.
type patchRequest struct {
	Schemas    []string         `json:"schemas"`
	Operations []patchOperation `json:"Operations"`
}

// patchOperation is one operation. Value stays raw because what is legal in
// it depends entirely on the path — and on the provider.
type patchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// The mapped attributes, as a patch path names them. Everything a directory
// may write is in this list, and nothing else is reachable.
const (
	attrUserName    = "username"
	attrExternalID  = "externalid"
	attrDisplayName = "displayname"
	attrEmail       = "emails"
	attrActive      = "active"
	// attrIgnored is the one attribute that is neither mapped nor refused:
	// a password. §2 ignores it rather than failing the operation, because a
	// directory configured to push passwords should not have its whole sync
	// break on an attribute this server was never going to store.
	attrIgnored = "-ignored-"
)

// patchFailure is a refusal with the scimType a provider must see.
type patchFailure struct {
	scimType string
	detail   string
}

func (e *patchFailure) Error() string { return e.scimType + ": " + e.detail }

func invalidPath(path string) *patchFailure {
	return &patchFailure{typeInvalidPath, fmt.Sprintf("unsupported patch path %q", path)}
}

func invalidValue(detail string) *patchFailure {
	return &patchFailure{typeInvalidValue, detail}
}

// applyPatch folds the operations onto the account's current attributes and
// returns what they should become. It never touches storage: the handler
// writes the result, so a patch that fails halfway writes nothing at all —
// which is what makes a rejected operation leave the account exactly as it
// was rather than half-applied.
func applyPatch(current userWrite, req patchRequest) (userWrite, *patchFailure) {
	if len(req.Operations) == 0 {
		return current, invalidValue("Operations must not be empty")
	}

	next := current
	for _, op := range req.Operations {
		var err *patchFailure
		if next, err = applyOne(next, op); err != nil {
			return current, err
		}
	}
	return next, nil
}

// applyOne applies a single operation.
//
// An operation with no path carries an object naming the attributes, which
// is Okta's shape; it is applied by recursing once per key, so the two
// shapes share every rule below rather than agreeing by coincidence.
func applyOne(w userWrite, op patchOperation) (userWrite, *patchFailure) {
	verb := strings.ToLower(strings.TrimSpace(op.Op))
	switch verb {
	case "add", "replace", "remove":
	default:
		return w, invalidValue(fmt.Sprintf("unsupported patch op %q", op.Op))
	}

	if strings.TrimSpace(op.Path) == "" {
		if verb == "remove" {
			// RFC 7644 §3.5.2.2 requires a path on remove, and honouring a
			// pathless one would mean guessing which attribute to clear.
			return w, invalidPath("")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(op.Value, &fields); err != nil {
			return w, invalidValue("a patch operation without a path needs an object value")
		}
		for name, value := range fields {
			var failure *patchFailure
			if w, failure = applyOne(w, patchOperation{Op: verb, Path: name, Value: value}); failure != nil {
				return w, failure
			}
		}
		return w, nil
	}

	attr, ok := normalizePath(op.Path)
	if !ok {
		return w, invalidPath(op.Path)
	}
	if attr == attrIgnored {
		return w, nil
	}
	if verb == "remove" {
		return removeAttribute(w, attr, op.Path)
	}
	return setAttribute(w, attr, op.Value)
}

// removeAttribute clears one attribute. userName and active have no empty
// value that means anything, so removing them is a refusal rather than a
// guess: an account with no userName is not addressable, and an account with
// no active flag is neither on nor off.
func removeAttribute(w userWrite, attr, path string) (userWrite, *patchFailure) {
	switch attr {
	case attrEmail:
		w.attrs.Email = nil
	case attrExternalID:
		w.attrs.ExternalID = nil
	case attrDisplayName:
		w.attrs.DisplayName = ""
	default:
		return w, invalidValue(fmt.Sprintf("%s cannot be removed", path))
	}
	return w, nil
}

// setAttribute applies an add or a replace. The two are one case on purpose:
// every mapped attribute here holds a single value — emails collapses to the
// one address §4 maps — so adding to it and replacing it are the same write,
// and pretending otherwise would mean inventing a multi-valued model the
// storage does not have.
func setAttribute(w userWrite, attr string, raw json.RawMessage) (userWrite, *patchFailure) {
	if attr == attrActive {
		active, err := decodeBool(raw)
		if err != nil {
			return w, err
		}
		w.active = active
		return w, nil
	}

	value, err := decodeString(attr, raw)
	if err != nil {
		return w, err
	}
	switch attr {
	case attrUserName:
		if value == "" {
			return w, invalidValue("userName must not be empty")
		}
		w.attrs.ScimUserName = value
	case attrExternalID:
		w.attrs.ExternalID = optional(value)
	case attrDisplayName:
		w.attrs.DisplayName = value
	case attrEmail:
		w.attrs.Email = optional(value)
	}
	return w, nil
}

// optional maps the empty string onto SQL NULL: a provider clearing an
// attribute by setting it to "" means the same thing as removing it, and
// storing an empty string in a unique column would make the second such
// account a spurious conflict.
func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// decodeBool reads a SCIM boolean leniently. A real boolean is the correct
// form; the quoted "True"/"False" strings are what Entra has been observed
// sending, and a sync that deactivates nobody because of a pair of quotes is
// the worst possible failure of this endpoint.
func decodeBool(raw json.RawMessage) (bool, *patchFailure) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, invalidValue("active must be a boolean")
}

// decodeString reads a value that maps onto a text column. emails accepts
// the three shapes a provider may send it in — a bare string, one complex
// value, or the whole multi-valued array — and collapses them the same way
// the create path does.
func decodeString(attr string, raw json.RawMessage) (string, *patchFailure) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	if attr != attrEmail {
		return "", invalidValue(fmt.Sprintf("%s must be a string", attr))
	}

	var single emailValue
	if err := json.Unmarshal(raw, &single); err == nil && single.Value != "" {
		return single.Value, nil
	}
	var many []emailValue
	if err := json.Unmarshal(raw, &many); err == nil {
		return primaryEmail(many), nil
	}
	return "", invalidValue("emails must be a string, an email object, or an array of them")
}

// normalizePath maps one patch path onto a mapped attribute. It is where
// every provider dialect is absorbed: casing, the User schema's URN prefix,
// and the value filters Entra puts on multi-valued attributes
// (emails[type eq "work"].value).
//
// ok is false for a path this server maps onto nothing — which includes
// every attribute that would be dangerous to guess at, roles and groups
// among them.
func normalizePath(path string) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(path))
	p = strings.TrimPrefix(p, strings.ToLower(userSchemaURN)+":")
	p = stripValueFilters(p)

	switch p {
	case "username":
		return attrUserName, true
	case "externalid":
		return attrExternalID, true
	case "active":
		return attrActive, true
	case "displayname", "name.formatted":
		return attrDisplayName, true
	case "emails", "emails.value":
		return attrEmail, true
	case "password":
		return attrIgnored, true
	default:
		return "", false
	}
}

// stripValueFilters removes the [ ... ] value selectors from a path, so
// emails[type eq "work"].value normalizes to emails.value. There is one
// email per account here, so which entry of a multi-valued attribute the
// provider meant does not change the answer.
//
// The scan honours double quotes, because a filter's literal may contain a
// closing bracket. An unterminated bracket consumes the rest of the path,
// which then matches nothing and is refused as an unsupported path — the
// safe direction.
func stripValueFilters(path string) string {
	var b strings.Builder
	depth, inQuotes := 0, false
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case inQuotes:
			if c == '"' {
				inQuotes = false
			}
		case c == '"' && depth > 0:
			inQuotes = true
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// patchStatus is the HTTP status a patch failure carries. Every scimType
// this decoder produces is a 400: they all describe a request that cannot be
// understood, never a conflict with stored state.
const patchStatus = http.StatusBadRequest
