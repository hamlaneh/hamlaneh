package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Message search. The text-search decision — substring matching over a
// normalized copy of the content, no stemming, with the Persian fold that
// maps the three visually identical letter pairs and drops the zero-width
// non-joiner — is a PRODUCT decision and is unchanged here. It is argued in
// full in the PostgreSQL tree's migrations/0006_message_search.up.sql; this
// tree's 0006 records what changes and what does not, and names the ceiling
// the change costs.
//
// What changes is only the acceleration. PostgreSQL puts the fold inside a
// GIN pg_trgm index expression and matches with ILIKE, so matching is
// indexed. SQLite has no trigram index, no translate(), and a lower() that
// folds ASCII only — so the same match cannot be written in SQL at all
// without changing the semantics. It is decided in Go instead: the statement
// selects the candidate rows, scoped by membership and not deleted, and each
// row's text is folded with storage.FoldSearchText and tested against the
// folded needle.
//
// Both halves of that comparison come from the PostgreSQL driver on purpose.
// storage.FoldSearchText is the fold migration 0006's index expression is
// pinned to by test, and storage.SearchSnippet is the splitter that makes a
// snippet reproduce the message exactly. A second fold or a second splitter
// living here would be a second set of semantics waiting to drift.

// searchScope is the row set both passes start from, and the whole
// authorization story: a message reaches either statement only through a
// channel_members row naming the caller, so a conversation the caller is not
// in contributes nothing — not a result, not a snippet, and not one to the
// count. It is an inner join, never a filter applied to rows already read,
// which is what makes the guarantee a property of the statement rather than
// of the code around it. Both statements below are built from this constant
// so neither can be written without it.
//
// The caller's id is bound here and AGAIN in searchLabelJoins: SQLite's
// placeholders are positional, so the one parameter PostgreSQL writes as $1
// in two places is two binds of the same value.
const searchScope = `
	FROM messages m
	JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = ?`

// searchPredicate is what remains of PostgreSQL's WHERE once the match moves
// into Go: the soft-delete rule.
//
// It is the answer to "what does search do with a deleted message" — nothing,
// its content is erased on delete, so it could not match a needle anyway —
// and it is what keeps an erased row out from under the one needle that
// matches every string, the needle that folds away to nothing. On PostgreSQL
// it also lets the planner use the partial index; there is no index here for
// it to help, and the rule is the same.
const searchPredicate = `
	WHERE m.deleted_at IS NULL`

// searchColumns is the result row, in the order scanSearchResult expects.
const searchColumns = `m.id, m.content, m.created_at,
	u.id, u.username, u.display_name,
	c.id, c.kind, c.slug,
	peer.id, peer.username, peer.display_name`

// searchLabelJoins resolve each hit's label, and only its label — they add no
// rows and take none away, because every one of them is on a key that exists.
// The peer join is caller-relative (one direct message names bob to alice and
// alice to bob), which is why a storage.SearchChannelRef may carry a DMPeer
// at all. Its bind of the caller's id is the second of the two described on
// searchScope.
const searchLabelJoins = `
	JOIN channels c ON c.id = m.channel_id
	JOIN users u ON u.id = m.author_id
	LEFT JOIN users peer
	       ON c.kind = 'dm'
	      AND peer.id = CASE WHEN c.dm_user_a = ? THEN c.dm_user_b ELSE c.dm_user_a END`

// The two page statements. Each cursor state gets its own statement rather
// than one with an optional bound, exactly as the PostgreSQL driver does.
//
// Neither carries a LIMIT, and that is the one structural difference from the
// other driver: whether a row belongs to the page is decided in Go, so SQL
// cannot count the page off. They are ordered scans that the loop in
// SearchMessages stops early instead.
//
// SQLite has no row values, so PostgreSQL's (created_at, id) < (?, ?) is
// spelled out as the two comparisons it means. Timestamps are fixed-width
// UTC text (codec.go), so both halves are plain column comparisons.
const (
	searchPageQuery = `SELECT ` + searchColumns + searchScope + searchLabelJoins + searchPredicate + `
	ORDER BY m.created_at DESC, m.id DESC`

	searchPageAfterQuery = `SELECT ` + searchColumns + searchScope + searchLabelJoins + searchPredicate + `
	  AND (m.created_at < ? OR (m.created_at = ? AND m.id < ?))
	ORDER BY m.created_at DESC, m.id DESC`
)

// searchCountQuery feeds the counting pass. It selects the content rather
// than count(*) because the match is not something SQL can decide here; the
// counting itself happens in searchTotal.
//
// It deliberately ignores the cursor — `total` labels the whole result set,
// not what is left of it — and it needs no ORDER BY, because a count does not
// care which matches it saw. The label joins are left out (a count needs no
// labels) but the scope is the same constant, so the count can no more escape
// membership than the page can.
const searchCountQuery = `SELECT m.content` + searchScope + searchPredicate

// SearchMessages returns one page of the caller's message search, plus the
// capped total the results column is labelled with.
//
// Scope is the caller: params.UserID is joined against channel_members inside
// both statements, so this function cannot be misused into leaking a
// conversation — there is no unscoped variant of it to reach for.
//
// Matching is case-insensitive substring, over text normalized for the
// Persian characters users type two ways (migration 0006). The needle is
// normalized by exactly the same rules, so both halves of a comparison always
// agree.
func (s *Store) SearchMessages(ctx context.Context, params storage.SearchMessagesParams) (storage.SearchPage, error) {
	// No LIKE escaping happens here, and none is needed: PostgreSQL wraps the
	// needle in wildcards and escapes it because it matches with ILIKE, while
	// here the needle is text that never becomes pattern syntax. A caller
	// searching for "100%" or "a_b" searches for those characters by
	// construction rather than by remembering to escape them.
	needle := storage.FoldSearchText(params.Query)

	// Two passes, because the two questions stop at different places. The
	// count ignores the cursor and stops once it has SearchTotalCap+1
	// matches, which is all a capped count ever needs to know. The page
	// starts at the cursor and stops at Limit+1 matches — the one row past
	// the page that answers "is there another". Neither bound can serve the
	// other, which is why the PostgreSQL driver also runs two statements.
	total, err := s.searchTotal(ctx, params.UserID, needle)
	if err != nil {
		return storage.SearchPage{}, fmt.Errorf("count search matches: %w", err)
	}

	query, args := searchPageArgs(params)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return storage.SearchPage{}, fmt.Errorf("search messages: %w", err)
	}
	defer closeRows(rows)

	// ponytail: linear scan per search — fine for a household's history, not
	// for an organization's. FTS5's trigram tokenizer is the recorded upgrade
	// path (ADR 012), behind this same method and changing no semantics.
	//
	// The scan is streamed and stopped, never collected: rows arrive
	// newest-first and the loop breaks the moment the page is full, so what
	// is held in memory is one page and what is read is one prefix of the
	// caller's own history.
	results := []storage.SearchResult{}
	for rows.Next() {
		res, content, scanErr := scanSearchResult(rows)
		if scanErr != nil {
			return storage.SearchPage{}, fmt.Errorf("search messages: %w", scanErr)
		}
		if !searchMatches(content, needle) {
			continue
		}
		res.Snippet = storage.SearchSnippet(content, needle)
		results = append(results, res)
		if len(results) > params.Limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return storage.SearchPage{}, fmt.Errorf("search messages: %w", err)
	}

	page := storage.SearchPage{
		Results: results,
		Total:   min(total, storage.SearchTotalCap),
		Capped:  total > storage.SearchTotalCap,
	}
	if len(results) > params.Limit {
		page.Results = results[:params.Limit]
		page.HasMore = true
	}
	return page, nil
}

// searchTotal counts the caller's matches, stopping one past the cap so a
// single integer answers both "how many" and "were there more than the cap"
// — the same trick PostgreSQL's LIMIT SearchTotalCap+1 subquery plays.
func (s *Store) searchTotal(ctx context.Context, userID uuid.UUID, needle string) (int, error) {
	rows, err := s.db.QueryContext(ctx, searchCountQuery, userID)
	if err != nil {
		return 0, err
	}
	defer closeRows(rows)

	total := 0
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return 0, err
		}
		if !searchMatches(content, needle) {
			continue
		}
		total++
		if total > storage.SearchTotalCap {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

// searchPageArgs picks the statement and arguments for one page.
//
// It asks for no probe limit, unlike the PostgreSQL helper of the same name:
// the "one row past the page" that answers HasMore is counted by the loop in
// SearchMessages, because SQL cannot tell a matching row from a candidate
// one here. The caller's id appears twice for the reason searchScope gives.
func searchPageArgs(params storage.SearchMessagesParams) (string, []any) {
	if params.After != nil {
		at := asTime(params.After.CreatedAt)
		return searchPageAfterQuery, []any{params.UserID, params.UserID, at, at, params.After.ID}
	}
	return searchPageQuery, []any{params.UserID, params.UserID}
}

// searchMatches reports whether text contains needle under migration 0006's
// fold. needle must already be folded, by the same function.
//
// This is the whole of what PostgreSQL expresses as ILIKE over a translate()
// expression. A needle that folds to nothing is contained in every string, so
// it matches everything and storage.SearchSnippet highlights none of it —
// which is what ILIKE '%%' does on the other driver, and the honest rendering
// of a search for no characters either way.
func searchMatches(text, needle string) bool {
	return strings.Contains(storage.FoldSearchText(text), needle)
}

// scanSearchResult scans one searchColumns row and returns the message's text
// alongside it rather than a finished snippet: the caller decides the match
// from that text and only then pays for the split, so a row that does not
// match costs one fold and nothing more.
//
// The peer columns are null on every named channel, and on any row whose
// channel is not a direct message.
func scanSearchResult(row rowScanner) (storage.SearchResult, string, error) {
	var (
		res                           storage.SearchResult
		content                       string
		slug                          sql.NullString
		peerID                        uuid.NullUUID
		peerUsername, peerDisplayName sql.NullString
	)
	err := row.Scan(
		&res.MessageID, &content, timeScan{dst: &res.CreatedAt},
		&res.Author.ID, &res.Author.Username, &res.Author.DisplayName,
		&res.Channel.ID, &res.Channel.Kind, &slug,
		&peerID, &peerUsername, &peerDisplayName,
	)
	if err != nil {
		return storage.SearchResult{}, "", err
	}
	res.Channel.Slug = stringPtr(slug)
	res.Channel.DMPeer = searchPeer(peerID, peerUsername, peerDisplayName)
	return res, content, nil
}

// searchPeer builds the DM peer out of the three columns searchLabelJoins
// resolves. They arrive together or not at all; all three are checked anyway
// rather than reading two on that argument.
func searchPeer(id uuid.NullUUID, username, displayName sql.NullString) *storage.DMPeer {
	if !id.Valid || !username.Valid || !displayName.Valid {
		return nil
	}
	return &storage.DMPeer{ID: id.UUID, Username: username.String, DisplayName: displayName.String}
}
