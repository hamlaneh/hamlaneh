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
	MentionTokenLen = len(mentionPrefix) + 36 + 1
)

// MentionsOf returns the ids a write's mention rows should name: the two
// sources, one per channel mode, chosen in the one place both drivers call so
// they cannot diverge on which.
//
// A plaintext message is parsed, as it always was. An encrypted one is taken
// from the envelope's declaration (ADR 014), because content is the empty
// string by contract there and parsing it derives nobody — the defect this
// closes was exactly that silence: the
// composer offered the picker, the recipient rendered the name, and the badge
// that reaches somebody not looking at the channel never fired.
//
// The envelope's presence, not the content's emptiness, is the switch. That
// matters: it means a message can never have two sources of mention truth, so
// an envelope arriving beside readable content — which the write path refuses
// upstream anyway — could not smuggle a second set of rows in behind the
// declaration.
//
// Neither source is trusted with membership. Both feed the same array
// parameter of the same statements, whose channel_members join drops an id
// that is not in the conversation and whose primary key collapses repeats.
func MentionsOf(mls *MessageMls, content string) []uuid.UUID {
	if mls != nil {
		return mls.Mentions
	}
	return ParseMentions(content)
}

// ParseMentions returns the user ids a message's content mentions, in the
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
func ParseMentions(content string) []uuid.UUID {
	var ids []uuid.UUID
	seen := make(map[uuid.UUID]struct{})

	for i := 0; i+MentionTokenLen <= len(content); {
		offset := strings.Index(content[i:], mentionPrefix)
		if offset < 0 {
			break
		}
		start := i + offset
		if start+MentionTokenLen > len(content) {
			break
		}

		id, ok := mentionAt(content[start : start+MentionTokenLen])
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
		i = start + MentionTokenLen
	}
	return ids
}

// mentionAt decodes one candidate — MentionTokenLen bytes starting at a
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
