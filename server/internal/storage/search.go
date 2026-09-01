package storage

import (
	"context"
	"fmt"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Message search. The text-search decision — trigram substring matching over
// a normalized copy of the content, rather than a tsvector in one language —
// is argued in full in migrations/0006_message_search.up.sql. Read that
// before changing anything here; the index and this file are one design.

// SearchTotalCap is how far the contract counts before it gives up: `total`
// is exact up to this many matches and `total_capped` reports that there
// were more (openapi.yaml, SearchPage). Counting is bounded because the
// count exists to label a results column — "4 results for deploy" — and
// nobody reads the difference between 900 matches and 1200.
const SearchTotalCap = 200

// SearchSnippetPart is one run of a result's text: the message's own
// characters, and whether the query matched them. Concatenating the Text of
// every part of a snippet reproduces the message exactly — the server never
// renders markup, and the design's highlight is drawn client-side from Match
// (openapi.yaml, SearchSnippet).
type SearchSnippetPart struct {
	Text  string
	Match bool
}

// SearchChannelRef is enough of a channel to label a result: "#deploys" for
// a named channel, the peer's name for a direct message.
//
// It is deliberately not a Channel. A Channel carries per-caller counts that
// only a caller-scoped read fills, and handing search's four columns back in
// that shape would publish four zeros nobody computed (see Channel).
type SearchChannelRef struct {
	ID   uuid.UUID
	Kind ChannelKind
	// Slug is nil exactly for a direct message, which DMPeer labels instead.
	Slug *string
	// DMPeer is the other participant, set exactly for a direct message.
	// "Other" is well defined here because a search is always somebody's.
	DMPeer *DMPeer
}

// SearchResult is one message hit: where it was said, by whom, when, and the
// message split into matched and unmatched runs.
type SearchResult struct {
	MessageID uuid.UUID
	Channel   SearchChannelRef
	Author    MessageAuthor
	CreatedAt time.Time
	Snippet   []SearchSnippetPart
}

// SearchMessagesParams control one page of SearchMessages. UserID is not a
// filter but the scope itself: it is joined against channel_members inside
// the query, so no row from a conversation this user is not in can reach the
// results, the count, or a snippet.
type SearchMessagesParams struct {
	UserID uuid.UUID
	// Query is the raw text the caller typed. Normalization and LIKE
	// escaping happen here, so no pattern syntax of the caller's survives.
	Query string
	After *MessageCursor
	Limit int
}

// SearchPage is one page of results plus the capped count above them.
type SearchPage struct {
	// Results descend by (created_at, id) — newest first, which is what a
	// chat search wants and the only order a trigram index can offer, since
	// substring matching produces no relevance score to rank by.
	Results []SearchResult
	// Total is the match count across every channel the caller is in,
	// counted up to SearchTotalCap. Capped reports that there were more.
	Total  int
	Capped bool
	// HasMore reports that a further page exists after this one.
	HasMore bool
}

// normalizedContent MUST stay equivalent to the index expression in
// migration 0006. If the two drift nothing breaks visibly: the query stops
// using the index and quietly becomes a sequential scan over every message
// on the instance. TestSearchUsesTrigramIndexIntegration is what catches it.
const normalizedContent = `translate(m.content, U&'\064A\0643\0629\200C', U&'\06CC\06A9\0647')`

// searchScope is the row set both statements start from, and the whole
// authorization story: a message reaches either query only through a
// channel_members row naming the caller, so a conversation the caller is not
// in contributes nothing — not a result, not a snippet, and not one to the
// count. It is an inner join, never a filter applied to rows already read,
// which is what makes the guarantee a property of the statement rather than
// of the code around it. Both statements below are built from this constant
// so neither can be written without it.
const searchScope = `
	FROM messages m
	JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = $1`

// searchPredicate is the match itself.
//
// The deleted_at test is both the answer to "what does search do with a
// deleted message" — nothing, its content is erased on delete, so it could
// not match a needle anyway — and what lets the planner use the partial
// index.
const searchPredicate = `
	WHERE m.deleted_at IS NULL
	  AND ` + normalizedContent + ` ILIKE $2 ESCAPE '\'`

// searchColumns is the result row, in the order scanSearchResult expects.
const searchColumns = `m.id, m.content, m.created_at,
	u.id, u.username, u.display_name,
	c.id, c.kind, c.slug,
	peer.id, peer.username, peer.display_name`

// searchLabelJoins resolve each hit's label, and only its label — they add
// no rows and take none away, because every one of them is on a key that
// exists. The peer join is caller-relative (one direct message names bob to
// alice and alice to bob), which is why a SearchChannelRef may carry a
// DMPeer at all.
const searchLabelJoins = `
	JOIN channels c ON c.id = m.channel_id
	JOIN users u ON u.id = m.author_id
	LEFT JOIN users peer
	       ON c.kind = 'dm'
	      AND peer.id = CASE WHEN c.dm_user_a = $1 THEN c.dm_user_b ELSE c.dm_user_a END`

// The two page statements. Like the history queries in messages.go, each
// cursor state gets its own statement rather than one with an optional
// bound, so the keyset comparison stays a range the planner can use instead
// of a filter hidden behind `$3 IS NULL OR ...`.
const (
	searchPageQuery = `SELECT ` + searchColumns + searchScope + searchLabelJoins + searchPredicate + `
	ORDER BY m.created_at DESC, m.id DESC
	LIMIT $3`

	searchPageAfterQuery = `SELECT ` + searchColumns + searchScope + searchLabelJoins + searchPredicate + `
	  AND (m.created_at, m.id) < ($3::timestamptz, $4::uuid)
	ORDER BY m.created_at DESC, m.id DESC
	LIMIT $5`
)

// searchCountQuery counts matches, stopping one past the cap so a single
// integer answers both "how many" and "were there more than the cap". It
// deliberately ignores the cursor: `total` labels the whole result set, not
// what is left of it. The label joins are left out — a count needs no labels
// — but the scope is the same constant, so the count can no more escape
// membership than the page can.
const searchCountQuery = `SELECT count(*) FROM (
	SELECT 1` + searchScope + searchPredicate + `
	LIMIT $3
) capped`

// SearchMessages returns one page of the caller's message search, plus the
// capped total the results column is labelled with.
//
// Scope is the caller: params.UserID is joined against channel_members
// inside both statements, so this function cannot be misused into leaking a
// conversation — there is no unscoped variant of it to reach for.
//
// Matching is case-insensitive substring, over text normalized for the
// Persian characters users type two ways (migration 0006). The needle is
// normalized by exactly the same rules, so both halves of a comparison
// always agree.
func (s *Store) SearchMessages(ctx context.Context, params SearchMessagesParams) (SearchPage, error) {
	// The wildcards are added here, so the parameter carries no pattern
	// syntax of its own and a caller searching for "100%" searches for the
	// character rather than for every message on the instance.
	needle := FoldSearchText(params.Query)
	pattern := "%" + escapeLike(needle) + "%"

	// The count runs on the same predicate as the page and ignores the
	// cursor, so the last page of a long result set still carries the honest
	// total for the query rather than the size of what is left.
	var total int
	err := s.pool.QueryRow(ctx, searchCountQuery,
		params.UserID, pattern, SearchTotalCap+1).Scan(&total)
	if err != nil {
		return SearchPage{}, fmt.Errorf("count search matches: %w", err)
	}

	// One row beyond the page answers "is there another page".
	query, args := searchPageArgs(params, pattern)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return SearchPage{}, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		result, scanErr := scanSearchResult(rows, needle)
		if scanErr != nil {
			return SearchPage{}, fmt.Errorf("search messages: %w", scanErr)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return SearchPage{}, fmt.Errorf("search messages: %w", err)
	}

	page := SearchPage{Results: results, Total: min(total, SearchTotalCap), Capped: total > SearchTotalCap}
	if len(results) > params.Limit {
		page.Results = results[:params.Limit]
		page.HasMore = true
	}
	return page, nil
}

// searchPageArgs picks the statement and arguments for one page, asking for
// one row past it.
func searchPageArgs(params SearchMessagesParams, pattern string) (string, []any) {
	probeLimit := params.Limit + 1
	if params.After != nil {
		return searchPageAfterQuery,
			[]any{params.UserID, pattern, params.After.CreatedAt, params.After.ID, probeLimit}
	}
	return searchPageQuery, []any{params.UserID, pattern, probeLimit}
}

// scanSearchResult scans one searchColumns row and splits its text around
// the needle. The peer columns are null on every named channel, and on any
// row whose channel is not a direct message.
func scanSearchResult(row pgx.Row, needle string) (SearchResult, error) {
	var (
		res                           SearchResult
		content                       string
		peerID                        *uuid.UUID
		peerUsername, peerDisplayName *string
	)
	err := row.Scan(
		&res.MessageID, &content, &res.CreatedAt,
		&res.Author.ID, &res.Author.Username, &res.Author.DisplayName,
		&res.Channel.ID, &res.Channel.Kind, &res.Channel.Slug,
		&peerID, &peerUsername, &peerDisplayName,
	)
	if err != nil {
		return SearchResult{}, err
	}
	// The peer's three columns arrive together or not at all; all three are
	// checked anyway rather than dereferencing two on that argument.
	if peerID != nil && peerUsername != nil && peerDisplayName != nil {
		res.Channel.DMPeer = &DMPeer{ID: *peerID, Username: *peerUsername, DisplayName: *peerDisplayName}
	}
	res.Snippet = SearchSnippet(content, needle)
	return res, nil
}

// SearchSnippet splits content into alternating unmatched and matched runs.
//
// It re-finds the needle in Go rather than asking PostgreSQL for a headline:
// ts_headline returns a truncated fragment wrapped in markup, and the
// contract's snippet is the message's own characters plus a flag
// (openapi.yaml, SearchSnippet). Concatenating every part reproduces content
// byte for byte, which is what the contract's "never HTML" rests on — there
// is nothing here for a message body to inject into.
//
// The snippet is the whole message. Messages are capped at 4000 characters
// and a page at 50 results, so a page is bounded; a windowed snippet would
// have to choose which occurrence to show, and the design highlights all of
// them.
//
// needle must already be folded. A needle that folds to nothing — a query of
// only zero-width non-joiners — matches every message in SQL (ILIKE '%%')
// and highlights none of it here, which is the honest rendering of a search
// for no characters.
func SearchSnippet(content, needle string) []SearchSnippetPart {
	target := []rune(needle)
	if len(target) == 0 {
		return []SearchSnippetPart{{Text: content}}
	}
	folded := foldSearchRunes(content)

	parts := []SearchSnippetPart{}
	plainFrom := 0 // byte offset in content where the current plain run began
	for i := 0; i+len(target) <= len(folded.runes); {
		if !slices.Equal(folded.runes[i:i+len(target)], target) {
			i++
			continue
		}
		start, end := folded.starts[i], folded.ends[i+len(target)-1]
		if start > plainFrom {
			parts = append(parts, SearchSnippetPart{Text: content[plainFrom:start]})
		}
		parts = append(parts, SearchSnippetPart{Text: content[start:end], Match: true})
		plainFrom = end
		i += len(target)
	}
	if plainFrom < len(content) {
		parts = append(parts, SearchSnippetPart{Text: content[plainFrom:]})
	}
	return parts
}

// FoldSearchText applies the search normalization to a string. It is the Go
// half of migration 0006's translate() expression plus the case folding
// ILIKE performs, and the two are pinned to each other by
// TestSearchFoldingMatchesSQLIntegration.
func FoldSearchText(s string) string {
	return string(foldSearchRunes(s).runes)
}

// foldedText is a string folded for matching, plus each surviving rune's
// byte span in the ORIGINAL string. The spans are what let a match found in
// folded text be highlighted in the text the message actually contains — the
// contract's snippet is the message's own characters, not the query's.
//
// A dropped rune has no entry, so its bytes fall into whichever run happens
// to surround it: inside a match if it sits between two matched runes,
// outside if it sits at the edge. Either way the parts concatenate back to
// the original exactly, and a highlighted run covers precisely the runes
// that matched.
type foldedText struct {
	runes  []rune
	starts []int
	ends   []int
}

func foldSearchRunes(s string) foldedText {
	out := foldedText{
		runes:  make([]rune, 0, len(s)),
		starts: make([]int, 0, len(s)),
		ends:   make([]int, 0, len(s)),
	}
	for i, r := range s {
		// The span is the ORIGINAL rune's, taken before folding: case folding
		// does not always preserve a rune's encoded width.
		start, end := i, i+utf8.RuneLen(r)
		switch r {
		case zeroWidthNonJoiner:
			continue
		case arabicYeh:
			r = farsiYeh
		case arabicKaf:
			r = keheh
		case tehMarbuta:
			r = heh
		default:
			// Matches what ILIKE does on the SQL side. Go and PostgreSQL can
			// disagree on exotic case mappings (dotted capital I and the
			// like); where they do, the row still matches and its snippet
			// simply carries no highlighted run.
			r = unicode.ToLower(r)
		}
		out.runes = append(out.runes, r)
		out.starts = append(out.starts, start)
		out.ends = append(out.ends, end)
	}
	return out
}

// The Persian characters users type two ways, written as escapes rather than
// literals: they are visually identical to their counterparts and one of
// them is invisible, so a literal here would be unreviewable. Migration 0006
// lists the same set with the reasoning behind it, and must stay in step.
const (
	arabicYeh          = '\u064A' // U+064A ARABIC LETTER YEH
	farsiYeh           = '\u06CC' // U+06CC ARABIC LETTER FARSI YEH
	arabicKaf          = '\u0643' // U+0643 ARABIC LETTER KAF
	keheh              = '\u06A9' // U+06A9 ARABIC LETTER KEHEH
	tehMarbuta         = '\u0629' // U+0629, the Arabic feminine ending
	heh                = '\u0647' // U+0647 ARABIC LETTER HEH
	zeroWidthNonJoiner = '\u200C' // U+200C ZERO WIDTH NON-JOINER
)
