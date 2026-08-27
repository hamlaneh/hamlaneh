package scim

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The filter language, in full (scim.md §2): equality on userName, or
// equality on externalId. Anything else is 400 invalidFilter.
//
// That is not a stub of RFC 7644's filter grammar — it is the whole of what
// a sync engine actually sends. Okta and Entra both look an account up by
// exactly one of these two before deciding whether to create it, and the
// rest of the grammar (co, sw, pr, and, or, not, parentheses, complex
// attribute paths) exists for query APIs this surface is not one of.
// Refusing it loudly is honest; implementing a parser for it and mapping
// half the results onto SQL would not be.
//
// A refusal here is also the safe direction: a filter this parser does not
// understand must never fall through to "no filter", which would answer a
// lookup for one person with the whole directory.

// errInvalidFilter is what every unparseable filter reports. The detail a
// caller sees is written at the handler, so the messages cannot drift apart.
var errInvalidFilter = errors.New("scim: unsupported filter")

// maxFilterLen bounds the filter expression. The longest legitimate one is
// an attribute name, an operator and a quoted email address; anything past
// this is not a filter anybody meant.
const maxFilterLen = 512

// parseFilter turns a SCIM filter expression into the storage filter. An
// empty expression is the empty filter — "every account" — which is what a
// provider's paging read of the whole directory asks for.
//
// The value is unmarshalled as a JSON string because that is exactly what
// RFC 7644 says it is, which means the escaping rules come from a library
// rather than from a hand-written unquoter. It also rejects compound
// expressions for free: the "and" in `userName eq "a" and active eq true`
// becomes trailing content after a complete JSON value, which is an error.
func parseFilter(raw string) (storage.ScimUserFilter, error) {
	expr := strings.TrimSpace(raw)
	if expr == "" {
		return storage.ScimUserFilter{}, nil
	}
	if len(expr) > maxFilterLen {
		return storage.ScimUserFilter{}, errInvalidFilter
	}

	attr, rest, found := strings.Cut(expr, " ")
	if !found {
		return storage.ScimUserFilter{}, errInvalidFilter
	}
	op, valueLiteral, found := strings.Cut(strings.TrimSpace(rest), " ")
	if !found || !strings.EqualFold(op, "eq") {
		return storage.ScimUserFilter{}, errInvalidFilter
	}

	var value string
	if err := json.Unmarshal([]byte(strings.TrimSpace(valueLiteral)), &value); err != nil {
		return storage.ScimUserFilter{}, errInvalidFilter
	}

	// SCIM attribute names are case-insensitive (RFC 7644 §3.4.2.2), and
	// providers do not agree on the casing they send — nor on whether they
	// qualify the name with the User schema's URN.
	switch strings.TrimPrefix(strings.ToLower(attr), strings.ToLower(userSchemaURN)+":") {
	case "username":
		return storage.ScimUserFilter{UserName: &value}, nil
	case "externalid":
		return storage.ScimUserFilter{ExternalID: &value}, nil
	default:
		return storage.ScimUserFilter{}, errInvalidFilter
	}
}
