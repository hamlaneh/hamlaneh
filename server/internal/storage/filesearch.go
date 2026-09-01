package storage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Filename search — the `files` tab of the same search column, and
// deliberately the same design as the message half next door in search.go:
// trigram substring matching over a normalized copy of the text, the same
// fold, the same snippet parts, the same capped total. Migration 0007's
// attachments_filename_search_idx is 0006's index with `filename` in place
// of `content`, so the two halves succeed and fail in the same ways and a
// user learns one search, not two.

// FileSearchResult is one filename hit: the attachment, plus the message it
// rode in on — which is where a click lands, so the message's id, author,
// channel and time are the same four the message half returns.
//
// SearchResult is embedded rather than copied so both tabs answer in one
// shape; Snippet here runs over the filename. Attachment is the whole stored
// row rather than a reduced copy of it, so a hit serializes through the same
// apiAttachment every other endpoint's file cards do — URLs included, which
// are minted per response and are never storage's to hand out (openapi.yaml,
// Attachment).
type FileSearchResult struct {
	SearchResult
	Attachment Attachment
}

// SearchFilesParams control one page of SearchFiles. Like
// SearchMessagesParams, UserID is not a filter but the scope itself: it is
// joined against channel_members inside the query, so no file from a
// conversation this user is not in can reach the results, the count, or a
// snippet.
type SearchFilesParams struct {
	UserID uuid.UUID
	// Query is the raw text the caller typed; normalization and LIKE
	// escaping happen here.
	Query string
	// After is the keyset cursor fileSearchCursor describes: a result's
	// CreatedAt paired with its ATTACHMENT's id, not its message's.
	After *MessageCursor
	Limit int
}

// FileSearchPage is one page of filename hits plus the capped count above
// them, in every respect SearchPage's shape.
type FileSearchPage struct {
	// Results descend by (message created_at, attachment id) — newest first,
	// the only order substring matching can offer.
	Results []FileSearchResult
	// Total is the match count across every channel the caller is in,
	// counted up to SearchTotalCap. Capped reports that there were more.
	Total  int
	Capped bool
	// HasMore reports that a further page exists after this one.
	HasMore bool
}

// normalizedFilename MUST stay equivalent to the index expression in
// migration 0007 — the same drift-into-a-sequential-scan hazard
// normalizedContent carries, and TestFileSearchUsesTrigramIndexIntegration
// is what catches it. It differs from normalizedContent only in the explicit
// lower(), which 0007's index has and 0006's leaves to ILIKE.
const normalizedFilename = `lower(translate(a.filename, U&'\064A\0643\0629\200C', U&'\06CC\06A9\0647'))`

// fileSearchScope is the whole authorization story, and it is literally the
// message half's searchScope with the files hung off it: an attachment
// reaches either statement below only through a message the caller shares a
// channel_members row with. Two consequences fall out of the join rather
// than out of a filter someone has to remember:
//
//   - an orphan — a file uploaded and never sent, message_id NULL — joins to
//     no message and so appears nowhere;
//   - a file whose message the caller cannot see is not a row that is read
//     and then dropped; it is a row the statement never produces.
//
// Membership is checked against the MESSAGE's channel, which is the channel
// that decides who may see the card.
//
// The third join is ADR 013's: an encrypted channel's files are stored under
// the literal placeholder `encrypted`, so indexing them here would make one
// query match every one of them and every other query match none. They are
// excluded by the join rather than by a filter, for the reason the membership
// join is a join — so no statement built from this constant can be written
// without it, the count included. The attachment's OWN channel is the one
// asked, because that is the channel it was born in and can never leave.
const fileSearchScope = searchScope + `
	JOIN attachments a ON a.message_id = m.id
	JOIN channels ac ON ac.id = a.channel_id AND NOT ac.e2ee`

// fileSearchPredicate is the match, plus the soft-delete rule: a deleted
// message's cards are gone with it. Deleting erases the content but leaves
// the attachments rows attached, so unlike the message half this test is
// carrying real weight rather than only helping the planner.
const fileSearchPredicate = `
	WHERE m.deleted_at IS NULL
	  AND ` + normalizedFilename + ` ILIKE $2 ESCAPE '\'`

// fileSearchColumns is the result row, in the order scanFileSearchResult
// expects: the message's four, its labels, then the file.
const fileSearchColumns = `m.id, m.created_at,
	u.id, u.username, u.display_name,
	c.id, c.kind, c.slug,
	peer.id, peer.username, peer.display_name,
	a.id, a.channel_id, a.uploader_id, a.message_id,
	a.filename, a.content_type, a.size_bytes,
	a.width, a.height, a.has_thumbnail, a.created_at`

// The keyset is (m.created_at, a.id), not the message half's (created_at,
// id): one message can carry several files, so paging on the message's id
// would drop every card after the one a page boundary fell on. The
// attachment's id is unique per row, which is what a cursor has to be.
const (
	fileSearchPageQuery = `SELECT ` + fileSearchColumns + fileSearchScope + searchLabelJoins + fileSearchPredicate + `
	ORDER BY m.created_at DESC, a.id DESC
	LIMIT $3`

	fileSearchPageAfterQuery = `SELECT ` + fileSearchColumns + fileSearchScope + searchLabelJoins + fileSearchPredicate + `
	  AND (m.created_at, a.id) < ($3::timestamptz, $4::uuid)
	ORDER BY m.created_at DESC, a.id DESC
	LIMIT $5`
)

// fileSearchCountQuery counts matches, stopping one past the cap, and
// ignores the cursor for the reason searchCountQuery does: `total` labels
// the whole result set, not what is left of it. Same scope constant, so the
// count can no more escape membership than the page can.
const fileSearchCountQuery = `SELECT count(*) FROM (
	SELECT 1` + fileSearchScope + fileSearchPredicate + `
	LIMIT $3
) capped`

// SearchFiles returns one page of the caller's filename search, plus the
// capped total the results column is labelled with.
//
// Scope is the caller, joined inside both statements — there is no unscoped
// variant of this function to reach for. Matching is the same
// case-insensitive substring over the same Persian fold the message half
// uses, so a query typed once searches both tabs alike.
func (s *Store) SearchFiles(ctx context.Context, params SearchFilesParams) (FileSearchPage, error) {
	// The wildcards are added here so the parameter carries no pattern
	// syntax of the caller's: a search for "report_final" looks for that
	// underscore rather than for any character.
	needle := FoldSearchText(params.Query)
	pattern := "%" + escapeLike(needle) + "%"

	var total int
	err := s.pool.QueryRow(ctx, fileSearchCountQuery,
		params.UserID, pattern, SearchTotalCap+1).Scan(&total)
	if err != nil {
		return FileSearchPage{}, fmt.Errorf("count file search matches: %w", err)
	}

	// One row beyond the page answers "is there another page".
	query, args := fileSearchPageArgs(params, pattern)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return FileSearchPage{}, fmt.Errorf("search files: %w", err)
	}
	defer rows.Close()

	results := []FileSearchResult{}
	for rows.Next() {
		result, scanErr := scanFileSearchResult(rows, needle)
		if scanErr != nil {
			return FileSearchPage{}, fmt.Errorf("search files: %w", scanErr)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return FileSearchPage{}, fmt.Errorf("search files: %w", err)
	}

	page := FileSearchPage{Results: results, Total: min(total, SearchTotalCap), Capped: total > SearchTotalCap}
	if len(results) > params.Limit {
		page.Results = results[:params.Limit]
		page.HasMore = true
	}
	return page, nil
}

// fileSearchPageArgs picks the statement and arguments for one page, asking
// for one row past it.
func fileSearchPageArgs(params SearchFilesParams, pattern string) (string, []any) {
	probeLimit := params.Limit + 1
	if params.After != nil {
		return fileSearchPageAfterQuery,
			[]any{params.UserID, pattern, params.After.CreatedAt, params.After.ID, probeLimit}
	}
	return fileSearchPageQuery, []any{params.UserID, pattern, probeLimit}
}

// scanFileSearchResult scans one fileSearchColumns row and splits the
// FILENAME around the needle — the same splitter the message half runs over
// a body, so the parts still concatenate back to the stored text exactly and
// still carry no markup.
func scanFileSearchResult(row pgx.Row, needle string) (FileSearchResult, error) {
	var (
		res                           FileSearchResult
		peerID                        *uuid.UUID
		peerUsername, peerDisplayName *string
	)
	err := row.Scan(
		&res.MessageID, &res.CreatedAt,
		&res.Author.ID, &res.Author.Username, &res.Author.DisplayName,
		&res.Channel.ID, &res.Channel.Kind, &res.Channel.Slug,
		&peerID, &peerUsername, &peerDisplayName,
		&res.Attachment.ID, &res.Attachment.ChannelID, &res.Attachment.UploaderID,
		&res.Attachment.MessageID,
		&res.Attachment.Filename, &res.Attachment.ContentType, &res.Attachment.SizeBytes,
		&res.Attachment.Width, &res.Attachment.Height, &res.Attachment.HasThumbnail,
		&res.Attachment.CreatedAt,
	)
	if err != nil {
		return FileSearchResult{}, err
	}
	// The peer's three columns arrive together or not at all; all three are
	// checked anyway rather than dereferencing two on that argument.
	if peerID != nil && peerUsername != nil && peerDisplayName != nil {
		res.Channel.DMPeer = &DMPeer{ID: *peerID, Username: *peerUsername, DisplayName: *peerDisplayName}
	}
	res.Snippet = SearchSnippet(res.Attachment.Filename, needle)
	return res, nil
}
