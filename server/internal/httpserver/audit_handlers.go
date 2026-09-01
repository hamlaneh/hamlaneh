package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// maxAuditActionLen bounds the action filter, matching the contract's
// maxLength and the column's own CHECK.
const maxAuditActionLen = 64

// WithAuditChain wires the verifying half of the audit log: the key this
// server checks a page against before it reports chain_valid. WithAudit
// (admin_handlers.go) wires the recording half, and both take the same
// key — one seals entries, the other re-computes those seals.
//
// Omitting it leaves the log unverifiable, and GET /api/v1/admin/audit then
// answers 500 rather than claiming a page it did not check. Production
// always passes one: main refuses to start without the key.
func WithAuditChain(chain *audit.Chain) Option {
	return func(s *apiServer) { s.auditChain = chain }
}

// ListAuditEntries returns one page of the audit log, newest first.
//
// chain_valid is the verification of the entries being returned. A false
// there means a row was edited or removed in the database itself, which no
// path through this server can do — it is not a display concern, and the
// failure is logged at error level for whatever watches the server's logs.
func (s *apiServer) ListAuditEntries(w http.ResponseWriter, r *http.Request, params api.ListAuditEntriesParams) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("audit list reached without principal"))
		return
	}
	if !authz.Can(r.Context(), &prin.user, authz.AdminAuditList, nil) {
		writeError(w, r, http.StatusForbidden, codeForbidden, msgForbidden)
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}
	if s.auditChain == nil {
		// A server with no chain cannot say whether what it reads back is
		// what was written, and a page that answers chain_valid without
		// checking would be worse than no page. Production always has one.
		internalError(w, r, errors.New("audit log reached with no chain configured"))
		return
	}

	listParams, ok := auditListParams(w, r, params)
	if !ok {
		return
	}

	// One row beyond the page, as every other listing does — and here it
	// earns a second job: it is the page's older neighbour, so the linkage
	// check below can see across the page boundary instead of stopping at
	// it.
	limit := listParams.Limit
	listParams.Limit = limit + 1
	entries, err := store.ListAuditEntries(r.Context(), listParams)
	if err != nil {
		internalError(w, r, err)
		return
	}

	verifyErr := s.verifyAudit(entries, listParams)
	if verifyErr != nil {
		slog.Error("audit chain verification failed",
			"error", verifyErr, "actor", prin.user.ID)
	}

	page := api.AuditPage{
		ChainValid: verifyErr == nil,
		Entries:    make([]api.AuditEntry, 0, min(len(entries), limit)),
	}
	if len(entries) > limit {
		entries = entries[:limit]
		next := encodeAuditCursor(entries[len(entries)-1])
		page.NextCursor = &next
	}
	for _, e := range entries {
		page.Entries = append(page.Entries, apiAuditEntry(r, e))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// verifyAudit checks what is about to be returned, with the stronger check
// where it applies.
//
// A filtered page is not a contiguous run of the chain — rows between its
// entries were left out on purpose — so all that can be asked of it is that
// each entry still matches its own hash. An unfiltered page is contiguous,
// and gets the linkage check too, which is the one that catches a deleted
// row.
func (s *apiServer) verifyAudit(entries []storage.AuditEntry, params storage.ListAuditParams) error {
	if params.Action != "" || params.ActorID != nil {
		return s.auditChain.Verify(entries)
	}
	return s.auditChain.VerifyRange(entries)
}

// auditListParams validates the query into storage terms. On a violation it
// answers 400 and reports false.
func auditListParams(w http.ResponseWriter, r *http.Request, params api.ListAuditEntriesParams) (storage.ListAuditParams, bool) {
	out := storage.ListAuditParams{Limit: defaultListLimit, ActorID: params.ActorId}

	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > maxListLimit {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "limit must be between 1 and 100")
			return storage.ListAuditParams{}, false
		}
		out.Limit = *params.Limit
	}
	if params.Action != nil {
		if len(*params.Action) > maxAuditActionLen {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "action must be at most 64 characters")
			return storage.ListAuditParams{}, false
		}
		out.Action = *params.Action
	}
	if params.Cursor != nil {
		cursor, err := decodeAuditCursor(*params.Cursor)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "invalid pagination cursor")
			return storage.ListAuditParams{}, false
		}
		out.Before = cursor
	}
	return out, true
}

// apiAuditEntry maps a stored entry onto the contract's AuditEntry. The
// chain's own fields — the two hashes and the sequence — deliberately do
// not cross this boundary: a client cannot verify them without the key, and
// chain_valid is the answer it gets instead.
func apiAuditEntry(r *http.Request, e storage.AuditEntry) api.AuditEntry {
	out := api.AuditEntry{
		Id:          e.ID,
		Action:      e.Action,
		TargetId:    e.TargetID,
		TargetLabel: e.TargetLabel,
		OccurredAt:  e.OccurredAt,
	}
	if e.Actor != nil {
		out.Actor = &api.UserSummary{
			Id:          e.Actor.ID,
			Username:    e.Actor.Username,
			DisplayName: e.Actor.DisplayName,
		}
	}
	if e.IP != nil {
		ip := e.IP.String()
		out.Ip = &ip
	}
	if len(e.Detail) > 0 {
		var detail map[string]any
		if err := json.Unmarshal(e.Detail, &detail); err != nil {
			// Nothing this server writes can fail to decode, so a row that
			// does is one somebody reached directly. The entry still lists —
			// hiding it would hide the evidence — and chain_valid is what
			// reports it.
			slog.Error("audit entry detail does not decode", "entry", e.ID, "path", r.URL.Path, "error", err)
		} else {
			out.Detail = &detail
		}
	}
	return out
}

// Audit cursors encode the keyset position (occurred_at, seq) of the last
// row of a page as base64url("RFC3339Nano|seq"), the same shape and for the
// same reason as the user cursors in user_handlers.go: RFC3339Nano keeps
// PostgreSQL's microsecond precision exactly, so the cursor round-trips.
func encodeAuditCursor(e storage.AuditEntry) string {
	raw := e.OccurredAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(e.Seq, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAuditCursor(encoded string) (*storage.AuditCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode cursor: %w", err)
	}
	occurredPart, seqPart, found := strings.Cut(string(raw), "|")
	if !found {
		return nil, errors.New("decode cursor: missing separator")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, occurredPart)
	if err != nil {
		return nil, fmt.Errorf("decode cursor timestamp: %w", err)
	}
	seq, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("decode cursor sequence: %w", err)
	}
	return &storage.AuditCursor{OccurredAt: occurredAt, Seq: seq}, nil
}
