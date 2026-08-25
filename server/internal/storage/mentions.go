package storage

import (
	"strings"

	"github.com/google/uuid"
)

// The mention wire format, from the Message.content contract in
// docs/api/openapi.yaml: the literal token <@{user_id}>, with the id in
// canonical uuid form. Because the id is always exactly 36 bytes there,
// a token is a fixed width, and uuid.Parse of a 36-byte string accepts the
// canonical form and nothing else — no urn: prefix, no braces, no dashless
// hex. The length check is therefore also the format check.
const (
	mentionPrefix   = "<@"
	mentionSuffix   = '>'
	mentionTokenLen = len(mentionPrefix) + 36 + 1
)

// parseMentions returns the user ids a message's content mentions, in the
// order they first appear and without repeats.
//
// Only tokens are parsed, never display names: names are not unique, not
// stable, and a Persian one cannot match the username pattern at all, which
// is why the composer's picker inserts the id and renders the name.
//
// The scan is deliberately not markdown-aware — a token inside inline code or
// a fenced block counts as a mention. Agreeing with the client's renderer
// would take a CommonMark parser on this side, and a backtick heuristic that
// merely approximates one fails in the worse direction: one unbalanced
// backtick earlier in a message would swallow a real mention, and nobody ever
// learns they were pinged. The simple rule's failure is the other way round —
// a badge on a mention that renders as literal text, which the reader can see
// and explain. If pasted payloads ever make that a real complaint, the fix is
// to share one CommonMark pass with the renderer, not to bolt a heuristic on
// here.
//
// Content is untrusted text, so the scan is total: it takes bytes (every byte
// of a token is ASCII, and no ASCII byte can appear inside a multi-byte UTF-8
// sequence, so Persian text around a token can neither hide nor forge one),
// it never treats a malformed token as a mention, and it is linear in the
// length of the content.
func parseMentions(content string) []uuid.UUID {
	var ids []uuid.UUID
	seen := make(map[uuid.UUID]struct{})

	for i := 0; i+mentionTokenLen <= len(content); {
		offset := strings.Index(content[i:], mentionPrefix)
		if offset < 0 {
			break
		}
		start := i + offset
		if start+mentionTokenLen > len(content) {
			break
		}

		id, ok := mentionAt(content[start : start+mentionTokenLen])
		if !ok {
			// No token can begin inside another's prefix, so resuming just
			// past this one's keeps the whole scan linear rather than
			// quadratic on input crafted out of "<@".
			i = start + len(mentionPrefix)
			continue
		}
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		i = start + mentionTokenLen
	}
	return ids
}

// mentionAt decodes one candidate — mentionTokenLen bytes starting at a
// prefix — into the id it names, and reports whether it is a mention at all.
func mentionAt(candidate string) (uuid.UUID, bool) {
	if candidate[len(candidate)-1] != mentionSuffix {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(candidate[len(mentionPrefix) : len(candidate)-1])
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}
