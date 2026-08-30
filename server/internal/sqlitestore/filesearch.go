package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Filename search — the `files` tab of the same search column, and
// deliberately the same design as the message half next door in search.go:
// substring matching over a normalized copy of the text, the same fold, the
// same snippet parts, the same capped total, so a user learns one search and
// not two. Migration 0007's fold is 0006's with `filename` in place of
// `content`, and here as there it is applied in Go rather than by an index
// this database has no way to build.

// fileSearchScope is the whole authorization story, and it is literally the
// message half's searchScope with the files hung off it: an attachment
// reaches either statement below only through a message the caller shares a
// channel_members row with. Two consequences fall out of the join rather than
// out of a filter someone has to remember:
//
//   - an orphan — a file uploaded and never sent, message_id NULL — joins to
//     no message and so appears nowhere;
//   - a file whose message the caller cannot see is not a row that is read
//     and then dropped; it is a row the statement never produces.
//
// Membership is checked against the MESSAGE's channel, which is the channel
// that decides who may see the card.
const fileSearchScope = searchScope + `
	JOIN attachments a ON a.message_id = m.id`

// fileSearchColumns is the result row, in the order scanFileSearchResult
// expects: the message's four, its labels, then the file.
const fileSearchColumns = `m.id, m.created_at,
	u.id, u.username, u.display_name,
	c.id, c.kind, c.slug,
	peer.id, peer.username, peer.display_name,
	a.id, a.channel_id, a.uploader_id, a.message_id,
	a.filename, a.content_type, a.size_bytes,
	a.width, a.height, a.has_thumbnail, a.created_at`

// The two page statements, and searchPredicate reused verbatim: with the
// match moved into Go, what is left of the WHERE is the soft-delete rule
// alone — and it carries more weight on this half than on the message half.
// Deleting erases a message's content but leaves its attachments rows
// attached, so this test is the only thing that takes a deleted message's
// cards away with it, rather than merely helping a planner.
//
// The keyset is (m.created_at, a.id), not the message half's (created_at,
// id): one message can carry several files, so paging on the message's id
// would drop every card after the one a page boundary fell on. The
// attachment's id is unique per row, which is what a cursor has to be. SQLite
// has no row values, so the tuple comparison is spelled out as the two
// comparisons it means. Neither statement carries a LIMIT, for the reason
// search.go's pair does not: the page is counted off in Go.
const (
	fileSearchPageQuery = `SELECT ` + fileSearchColumns + fileSearchScope + searchLabelJoins + searchPredicate + `
	ORDER BY m.created_at DESC, a.id DESC`

	fileSearchPageAfterQuery = `SELECT ` + fileSearchColumns + fileSearchScope + searchLabelJoins + searchPredicate + `
	  AND (m.created_at < ? OR (m.created_at = ? AND a.id < ?))
	ORDER BY m.created_at DESC, a.id DESC`
)

// fileSearchCountQuery feeds the counting pass: the filename, because the
// match is not something SQL can decide here. It ignores the cursor for the
// reason searchCountQuery does — `total` labels the whole result set, not
// what is left of it — and uses the same scope constant, so the count can no
// more escape membership than the page can.
const fileSearchCountQuery = `SELECT a.filename` + fileSearchScope + searchPredicate

// SearchFiles returns one page of the caller's filename search, plus the
// capped total the results column is labelled with.
//
// Scope is the caller, joined inside both statements — there is no unscoped
// variant of this function to reach for. Matching is the same
// case-insensitive substring over the same Persian fold the message half
// uses, so a query typed once searches both tabs alike.
func (s *Store) SearchFiles(ctx context.Context, params storage.SearchFilesParams) (storage.FileSearchPage, error) {
	// As on the message half, no LIKE escaping is needed: the needle is text
	// that never becomes pattern syntax, so a search for "report_final" looks
	// for that underscore by construction.
	needle := storage.FoldSearchText(params.Query)

	// Two passes, for the reason SearchMessages gives: the count ignores the
	// cursor and stops at SearchTotalCap+1 matches, while the page starts at
	// the cursor and stops at Limit+1, and neither bound serves the other.
	total, err := s.fileSearchTotal(ctx, params.UserID, needle)
	if err != nil {
		return storage.FileSearchPage{}, fmt.Errorf("count file search matches: %w", err)
	}

	query, args := fileSearchPageArgs(params)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return storage.FileSearchPage{}, fmt.Errorf("search files: %w", err)
	}
	defer rows.Close()

	// ponytail: linear scan per search — fine for a household's history, not
	// for an organization's. FTS5's trigram tokenizer is the recorded upgrade
	// path (ADR 012), behind this same method and changing no semantics.
	//
	// Streamed and stopped, never collected: rows arrive newest-first and the
	// loop breaks the moment the page is full.
	results := []storage.FileSearchResult{}
	for rows.Next() {
		res, scanErr := scanFileSearchResult(rows)
		if scanErr != nil {
			return storage.FileSearchPage{}, fmt.Errorf("search files: %w", scanErr)
		}
		if !searchMatches(res.Attachment.Filename, needle) {
			continue
		}
		// The same splitter the message half runs over a body, run over the
		// filename: the parts still concatenate back to the stored text
		// exactly and still carry no markup.
		res.Snippet = storage.SearchSnippet(res.Attachment.Filename, needle)
		results = append(results, res)
		if len(results) > params.Limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return storage.FileSearchPage{}, fmt.Errorf("search files: %w", err)
	}

	page := storage.FileSearchPage{
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

// fileSearchTotal counts the caller's filename matches, stopping one past the
// cap so a single integer answers both "how many" and "were there more".
func (s *Store) fileSearchTotal(ctx context.Context, userID uuid.UUID, needle string) (int, error) {
	rows, err := s.db.QueryContext(ctx, fileSearchCountQuery, userID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	total := 0
	for rows.Next() {
		var filename string
		if err := rows.Scan(&filename); err != nil {
			return 0, err
		}
		if !searchMatches(filename, needle) {
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

// fileSearchPageArgs picks the statement and arguments for one page. Like
// searchPageArgs it asks for no probe limit — the row past the page is
// counted in Go — and binds the caller's id twice, once for the scope join
// and once for the caller-relative peer join.
func fileSearchPageArgs(params storage.SearchFilesParams) (string, []any) {
	if params.After != nil {
		at := asTime(params.After.CreatedAt)
		return fileSearchPageAfterQuery, []any{params.UserID, params.UserID, at, at, params.After.ID}
	}
	return fileSearchPageQuery, []any{params.UserID, params.UserID}
}

// scanFileSearchResult scans one fileSearchColumns row. The filename it needs
// for both the match and the snippet is part of the result already, so unlike
// scanSearchResult it returns nothing beside it.
func scanFileSearchResult(row rowScanner) (storage.FileSearchResult, error) {
	var (
		res                           storage.FileSearchResult
		slug                          sql.NullString
		peerID                        uuid.NullUUID
		peerUsername, peerDisplayName sql.NullString
		width, height                 sql.NullInt64
		// The domain models message_id as optional because an orphan has
		// none, but fileSearchScope joins on it, so every row this statement
		// produces has one. Scanned as a plain id and pointed at afterwards.
		messageID uuid.UUID
	)
	err := row.Scan(
		&res.MessageID, timeScan{dst: &res.CreatedAt},
		&res.Author.ID, &res.Author.Username, &res.Author.DisplayName,
		&res.Channel.ID, &res.Channel.Kind, &slug,
		&peerID, &peerUsername, &peerDisplayName,
		&res.Attachment.ID, &res.Attachment.ChannelID, &res.Attachment.UploaderID,
		&messageID,
		&res.Attachment.Filename, &res.Attachment.ContentType, &res.Attachment.SizeBytes,
		&width, &height, &res.Attachment.HasThumbnail,
		timeScan{dst: &res.Attachment.CreatedAt},
	)
	if err != nil {
		return storage.FileSearchResult{}, err
	}
	res.Attachment.MessageID = &messageID
	res.Channel.Slug = stringPtr(slug)
	res.Channel.DMPeer = searchPeer(peerID, peerUsername, peerDisplayName)
	res.Attachment.Width = attachmentIntPtr(width)
	res.Attachment.Height = attachmentIntPtr(height)
	return res, nil
}
