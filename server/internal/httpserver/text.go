package httpserver

import (
	"strings"
	"unicode/utf8"
)

// Free text the contract accepts from a client — message content, a channel
// topic, a search needle — is validated here before it reaches storage.
//
// This is validation, not sanitization, and the difference is the whole
// design. The server stores markdown **as authored**, because that is what
// the contract says a message is, and it never renders it: the web client
// renders through react-markdown with a strict allowlist and raw HTML never
// parsed, and search snippets are a parts array of {text, match} that the
// client draws — the server emits no markup anywhere. Stripping or rewriting
// markdown on the way in would corrupt what somebody wrote to defend a
// rendering step this server does not perform, and it would be irreversible.
//
// What the server does owe is that text it accepts can actually be stored and
// handed back. That is a narrower promise and it was being broken: a message
// containing a NUL passed every check here and failed inside PostgreSQL with
// "invalid byte sequence for encoding UTF8", which the handler could only
// answer as a 500. Malformed input is the client's mistake and belongs in a
// 400; a 500 says the server broke, and buries a real fault in noise.

// controlRunes are the characters that cannot be stored, or that carry no
// meaning in a single field of user text.
//
// PostgreSQL text cannot hold a NUL at all. The rest of the C0 block is
// terminal control, not writing — a bell or a backspace in a message body is
// either a mistake or an attempt to garble a log that renders it. Tab,
// newline and carriage return are exempt: a multi-line message is ordinary,
// and markdown is built on them.
//
// Nothing above C0 is refused. C1, bidi overrides and zero-width characters
// all have legitimate uses in the languages this product is written for —
// Persian is full of the zero-width non-joiner, and refusing bidi controls
// would break the isolation the UI itself applies to names.
func hasControlRunes(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\r':
			return false
		default:
			return r < 0x20 || r == 0x7f
		}
	})
}

// storableText reports whether s can be persisted and returned unchanged: it
// must be well-formed UTF-8 and carry no control runes.
//
// Well-formedness is checked even though Go strings arriving through the JSON
// decoder are already valid — the decoder replaces malformed bytes with
// U+FFFD rather than failing, so a caller cannot smuggle raw invalid UTF-8
// through it today. The check is here because that is a property of one
// decoder rather than of this function's contract, and the cost of not
// relying on it is one call.
func storableText(s string) bool {
	return utf8.ValidString(s) && !hasControlRunes(s)
}
