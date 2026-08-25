package storage_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Three canonical uuids to build mention tokens from. The wire format is the
// literal <@{user_id}> (docs/api/openapi.yaml, Message.content).
const (
	mentionAliceID = "550e8400-e29b-41d4-a716-446655440000"
	mentionBobID   = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	mentionCarolID = "1b4e28ba-2fa1-11d2-883f-0016d3cca427"
)

func mentionToken(id string) string { return "<@" + id + ">" }

type mentionParseCase struct {
	name    string
	content string
	want    []uuid.UUID
}

// mentionParseCases is both the table of TestParseMentions and the seed
// corpus of FuzzParseMentions, so the interesting inputs cannot drift apart
// from the ones the fuzzer starts out from.
var mentionParseCases = []mentionParseCase{
	{
		name:    "empty content",
		content: "",
	},
	{
		name:    "prose with no token",
		content: "standup in five minutes, same link as always",
	},
	{
		name:    "one mention",
		content: "hey " + mentionToken(mentionAliceID) + " look at this",
		want:    []uuid.UUID{uuid.MustParse(mentionAliceID)},
	},
	{
		name: "several mentions in one message",
		content: mentionToken(mentionAliceID) + " " + mentionToken(mentionBobID) +
			" can one of you take " + mentionToken(mentionCarolID) + "'s review?",
		want: []uuid.UUID{
			uuid.MustParse(mentionAliceID),
			uuid.MustParse(mentionBobID),
			uuid.MustParse(mentionCarolID),
		},
	},
	{
		name: "the same person twice is one id",
		content: mentionToken(mentionAliceID) + " and again " + mentionToken(mentionAliceID) +
			" because it is urgent",
		want: []uuid.UUID{uuid.MustParse(mentionAliceID)},
	},
	{
		name:    "content that is nothing but tokens",
		content: mentionToken(mentionAliceID) + mentionToken(mentionBobID) + mentionToken(mentionCarolID),
		want: []uuid.UUID{
			uuid.MustParse(mentionAliceID),
			uuid.MustParse(mentionBobID),
			uuid.MustParse(mentionCarolID),
		},
	},
	{
		name:    "surrounding punctuation does not hide a token",
		content: "(" + mentionToken(mentionAliceID) + "), " + mentionToken(mentionBobID) + "!",
		want: []uuid.UUID{
			uuid.MustParse(mentionAliceID),
			uuid.MustParse(mentionBobID),
		},
	},
	{
		// The U+200C between می and بینی is a zero-width non-joiner: ordinary
		// Persian orthography, not a hidden control character smuggled into a
		// test. Escaping it would make the line unreadable to the people who
		// have to check the Persian, so it stays literal.
		//
		//nolint:staticcheck // ST1018: U+200C is required Persian spelling here.
		name:    "Persian text around a token",
		content: "سلام " + mentionToken(mentionAliceID) + "، این تیکت رو می‌بینی؟", //nolint:staticcheck // ST1018: the U+200C is required Persian spelling, not a hidden control character.
		want:    []uuid.UUID{uuid.MustParse(mentionAliceID)},
	},
	{
		// The documented decision, pinned here: the parser is not
		// markdown-aware. See parseMentions on why.
		name:    "a token in inline code is still a mention",
		content: "the wire format is `" + mentionToken(mentionAliceID) + "`",
		want:    []uuid.UUID{uuid.MustParse(mentionAliceID)},
	},
	{
		name:    "a token in a fenced code block is still a mention",
		content: "```json\n{\"content\": \"" + mentionToken(mentionBobID) + "\"}\n```",
		want:    []uuid.UUID{uuid.MustParse(mentionBobID)},
	},
	{
		name:    "a malformed uuid is not a token",
		content: "<@550e8400-e29b-41d4-a716-44665544000g> <@550e8400e29b-41d4-a716-446655440000>",
	},
	{
		name:    "a uuid of the wrong length is not a token",
		content: "<@550e8400-e29b-41d4-a716-44665544000> <@550e8400-e29b-41d4-a716-4466554400000>",
	},
	{
		name:    "a uuid without dashes is not a token",
		content: "<@550e8400e29b41d4a716446655440000>",
	},
	{
		name: "the urn and braced uuid forms are not tokens",
		content: "<@urn:uuid:550e8400-e29b-41d4-a716-446655440000> " +
			"<@{550e8400-e29b-41d4-a716-446655440000}>",
	},
	{
		name:    "an unterminated token is not a token",
		content: "<@" + mentionAliceID + " <@ <",
	},
	{
		name:    "a token broken by a newline is not a token",
		content: "<@550e8400-e29b-41d4-a716-4466554400\n0>",
	},
	{
		name:    "a doubled prefix still yields the token inside it",
		content: "<@" + mentionToken(mentionAliceID),
		want:    []uuid.UUID{uuid.MustParse(mentionAliceID)},
	},
	{
		name:    "an uppercase uuid is the same id",
		content: mentionToken(strings.ToUpper(mentionAliceID)),
		want:    []uuid.UUID{uuid.MustParse(mentionAliceID)},
	},
	{
		// Well-formed and belongs to nobody. Which ids survive is the
		// database's call, not the parser's — see CreateMessage.
		name:    "the nil uuid is a well-formed token",
		content: mentionToken(uuid.Nil.String()),
		want:    []uuid.UUID{uuid.Nil},
	},
}

func TestParseMentions(t *testing.T) {
	t.Parallel()

	for _, tt := range mentionParseCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := storage.ParseMentions(tt.content)
			if !slices.Equal(got, tt.want) {
				t.Errorf("ParseMentions(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// FuzzParseMentions asserts the three properties that must hold for any
// input at all, not merely the absence of a panic: every id the parser
// returns is actually written as a well-formed token in the content it was
// given, no id comes back twice, and the number of ids is bounded by how
// many tokens the content has room for.
func FuzzParseMentions(f *testing.F) {
	for _, tt := range mentionParseCases {
		f.Add(tt.content)
	}

	f.Fuzz(func(t *testing.T, content string) {
		ids := storage.ParseMentions(content)

		if bound := len(content) / storage.MentionTokenLen; len(ids) > bound {
			t.Fatalf("ParseMentions(%q) returned %d ids; %d bytes hold at most %d tokens",
				content, len(ids), len(content), bound)
		}

		// A token is ASCII, and uuid.Parse accepts either case, so the
		// canonical lowercase form of every id must appear in the lowercased
		// content.
		lowered := strings.ToLower(content)
		seen := make(map[uuid.UUID]struct{}, len(ids))
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				t.Fatalf("ParseMentions(%q) returned %s twice", content, id)
			}
			seen[id] = struct{}{}

			if token := mentionToken(id.String()); !strings.Contains(lowered, token) {
				t.Fatalf("ParseMentions(%q) returned %s, but %s appears nowhere in it",
					content, id, token)
			}
		}
	})
}
