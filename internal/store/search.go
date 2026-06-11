package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/rafiulalam/ticketd/internal/domain"
)

// SearchParams filters and full-text-searches tickets.
type SearchParams struct {
	Query   string
	Status  string
	Project string
	Label   string
	Limit   int
}

// SearchResult is a compact match for list rendering.
type SearchResult struct {
	Key          string
	Title        string
	Status       domain.Status
	Priority     domain.Priority
	CommentCount int
	MatchedTitle bool // matched in title/description
	MatchedBody  bool // matched in a comment
	Snippet      string
}

// sanitizeFTS wraps each whitespace-separated term in double quotes so user
// text cannot inject FTS5 operators. Embedded quotes are escaped by doubling.
func sanitizeFTS(query string) string {
	fields := strings.Fields(query)
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		quoted = append(quoted, `"`+f+`"`)
	}
	return strings.Join(quoted, " ")
}

// Search runs full-text search across ticket title/description and comment
// bodies, merges hits per ticket, applies filters, and ranks by bm25.
func (s *Store) Search(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	// Build the candidate id set. With a query, gather matching ticket ids
	// from both FTS tables; without, fall back to a filtered scan.
	type hit struct {
		matchedTitle bool
		matchedBody  bool
		snippet      string
		rank         float64
	}
	hits := map[int64]*hit{}

	if strings.TrimSpace(p.Query) != "" {
		fts := sanitizeFTS(p.Query)

		// Title/description matches.
		trows, err := s.db.QueryContext(ctx,
			`SELECT rowid, bm25(tickets_fts) FROM tickets_fts WHERE tickets_fts MATCH ?`, fts)
		if err != nil {
			return nil, fmt.Errorf("ticket fts: %w", err)
		}
		for trows.Next() {
			var id int64
			var rank float64
			if err := trows.Scan(&id, &rank); err != nil {
				trows.Close()
				return nil, err
			}
			h := hits[id]
			if h == nil {
				h = &hit{rank: rank}
				hits[id] = h
			}
			h.matchedTitle = true
			if rank < h.rank {
				h.rank = rank
			}
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return nil, err
		}

		// Comment-body matches, mapped back to their ticket.
		crows, err := s.db.QueryContext(ctx,
			`SELECT c.ticket_id, bm25(comments_fts), snippet(comments_fts, 0, '', '', '…', 8)
			 FROM comments_fts
			 JOIN comments c ON c.id = comments_fts.rowid
			 WHERE comments_fts MATCH ?`, fts)
		if err != nil {
			return nil, fmt.Errorf("comment fts: %w", err)
		}
		for crows.Next() {
			var id int64
			var rank float64
			var snip string
			if err := crows.Scan(&id, &rank, &snip); err != nil {
				crows.Close()
				return nil, err
			}
			h := hits[id]
			if h == nil {
				h = &hit{rank: rank}
				hits[id] = h
			}
			h.matchedBody = true
			if h.snippet == "" {
				h.snippet = strings.TrimSpace(snip)
			}
			if rank < h.rank {
				h.rank = rank
			}
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return nil, err
		}

		if len(hits) == 0 {
			return nil, nil
		}
	}

	// Now load ticket rows, applying filters, and attach hit metadata.
	var where []string
	var args []any
	if len(hits) > 0 {
		placeholders := make([]string, 0, len(hits))
		for id := range hits {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		where = append(where, "t.id IN ("+strings.Join(placeholders, ",")+")")
	}
	if p.Status != "" {
		st, err := domain.ParseStatus(p.Status)
		if err != nil {
			return nil, err
		}
		where = append(where, "t.status = ?")
		args = append(args, string(st))
	}
	if p.Project != "" {
		where = append(where, "t.project = ?")
		args = append(args, p.Project)
	}
	if p.Label != "" {
		// Labels are a JSON array; match membership via EXISTS over json_each.
		where = append(where, "EXISTS (SELECT 1 FROM json_each(t.labels) WHERE json_each.value = ?)")
		args = append(args, p.Label)
	}

	q := `SELECT t.id, t.key, t.title, t.status, t.priority,
	             (SELECT COUNT(*) FROM comments c WHERE c.ticket_id = t.id)
	      FROM tickets t`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// Without an FTS query, order by priority then recency; with one we sort
	// by rank in Go after the scan.
	if len(hits) == 0 {
		q += " ORDER BY t.priority ASC, t.updated_at DESC LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	type ranked struct {
		res  SearchResult
		rank float64
	}
	var rankedList []ranked
	for rows.Next() {
		var id int64
		var r SearchResult
		var status string
		var priority int
		if err := rows.Scan(&id, &r.Key, &r.Title, &status, &priority, &r.CommentCount); err != nil {
			return nil, err
		}
		r.Status = domain.Status(status)
		r.Priority = domain.Priority(priority)
		if h, ok := hits[id]; ok {
			r.MatchedTitle = h.matchedTitle
			r.MatchedBody = h.matchedBody
			r.Snippet = h.snippet
			rankedList = append(rankedList, ranked{r, h.rank})
		} else {
			results = append(results, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(hits) > 0 {
		// Stable sort by bm25 rank ascending (more negative = better).
		for i := 1; i < len(rankedList); i++ {
			for j := i; j > 0 && rankedList[j].rank < rankedList[j-1].rank; j-- {
				rankedList[j], rankedList[j-1] = rankedList[j-1], rankedList[j]
			}
		}
		for _, rk := range rankedList {
			results = append(results, rk.res)
			if len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}

// ContextReport is the structured backing for the get_context tool.
type ContextReport struct {
	Project    string // "" means all projects
	InProgress []ContextTicket
	Blocked    []BlockedTicket
	NextUp     []ContextTicket
	BacklogN   int
	DoneN      int
}

// ContextTicket is a ticket plus its most recent worklog excerpt.
type ContextTicket struct {
	Key          string
	Title        string
	Priority     domain.Priority
	Status       domain.Status
	ParentKey    string
	LastActivity string // RFC3339-ish display time of last comment
	LastComment  string
	Stale        bool // in_progress/in_review and untouched beyond StaleAfter
	StaleDays    int  // whole days since updated_at, when Stale
}

// StaleAfter is how long an in-progress ticket may go untouched before
// get_context flags it (design open question #3).
const StaleAfter = 7 * 24 * time.Hour

// BlockedTicket names a blocked ticket and what it is blocked on.
type BlockedTicket struct {
	Key       string
	Title     string
	BlockedOn []domain.Link // tickets this one is blocked by (incoming blocks)
}

// ContextReport assembles the working-state report. project == "" spans all.
func (s *Store) ContextReport(ctx context.Context, project string) (ContextReport, error) {
	rep := ContextReport{Project: project}

	projClause := ""
	var projArg []any
	if project != "" {
		projClause = " AND project = ?"
		projArg = []any{project}
	}

	// In-progress + in-review tickets, with last comment.
	ipRows, err := s.db.QueryContext(ctx,
		`SELECT key, title, priority, status, updated_at FROM tickets
		 WHERE status IN ('in_progress','in_review')`+projClause+`
		 ORDER BY priority ASC, updated_at DESC`, projArg...)
	if err != nil {
		return rep, err
	}
	cutoff := now().Add(-StaleAfter)
	var ipKeys []ContextTicket
	for ipRows.Next() {
		var ct ContextTicket
		var status, updatedAt string
		var pr int
		if err := ipRows.Scan(&ct.Key, &ct.Title, &pr, &status, &updatedAt); err != nil {
			ipRows.Close()
			return rep, err
		}
		ct.Priority = domain.Priority(pr)
		ct.Status = domain.Status(status)
		if u := parseTime(updatedAt); !u.IsZero() && u.Before(cutoff) {
			ct.Stale = true
			ct.StaleDays = int(now().Sub(u).Hours() / 24)
		}
		ipKeys = append(ipKeys, ct)
	}
	ipRows.Close()
	if err := ipRows.Err(); err != nil {
		return rep, err
	}
	for _, ct := range ipKeys {
		var body, created string
		err := s.db.QueryRowContext(ctx,
			`SELECT body, created_at FROM comments
			 WHERE ticket_id = (SELECT id FROM tickets WHERE key = ?)
			 ORDER BY created_at DESC, id DESC LIMIT 1`, ct.Key).Scan(&body, &created)
		if err == nil {
			ct.LastComment = body
			ct.LastActivity = created
		} else if err != sql.ErrNoRows {
			return rep, err
		}
		rep.InProgress = append(rep.InProgress, ct)
	}

	// Blocked tickets and what blocks them (incoming 'blocks' links).
	bRows, err := s.db.QueryContext(ctx,
		`SELECT key, title FROM tickets WHERE status = 'blocked'`+projClause+`
		 ORDER BY priority ASC, updated_at DESC`, projArg...)
	if err != nil {
		return rep, err
	}
	var blockedKeys []BlockedTicket
	for bRows.Next() {
		var bt BlockedTicket
		if err := bRows.Scan(&bt.Key, &bt.Title); err != nil {
			bRows.Close()
			return rep, err
		}
		blockedKeys = append(blockedKeys, bt)
	}
	bRows.Close()
	if err := bRows.Err(); err != nil {
		return rep, err
	}
	for i := range blockedKeys {
		bt := &blockedKeys[i]
		lrows, err := s.db.QueryContext(ctx,
			`SELECT bl.key, bl.title FROM links l
			 JOIN tickets blocked ON blocked.id = l.to_id
			 JOIN tickets bl ON bl.id = l.from_id
			 WHERE blocked.key = ? AND l.kind = 'blocks'`, bt.Key)
		if err != nil {
			return rep, err
		}
		for lrows.Next() {
			var ln domain.Link
			ln.Kind = domain.LinkBlocks
			if err := lrows.Scan(&ln.ToKey, &ln.ToTitle); err != nil {
				lrows.Close()
				return rep, err
			}
			bt.BlockedOn = append(bt.BlockedOn, ln)
		}
		lrows.Close()
		if err := lrows.Err(); err != nil {
			return rep, err
		}
		rep.Blocked = append(rep.Blocked, *bt)
	}

	// Next up: top todo tickets by priority.
	nRows, err := s.db.QueryContext(ctx,
		`SELECT t.key, t.title, t.priority,
		        (SELECT p.key FROM tickets p WHERE p.id = t.parent_id)
		 FROM tickets t WHERE t.status = 'todo'`+projClause+`
		 ORDER BY t.priority ASC, t.updated_at DESC LIMIT 5`, projArg...)
	if err != nil {
		return rep, err
	}
	for nRows.Next() {
		var ct ContextTicket
		var pr int
		var parent sql.NullString
		if err := nRows.Scan(&ct.Key, &ct.Title, &pr, &parent); err != nil {
			nRows.Close()
			return rep, err
		}
		ct.Priority = domain.Priority(pr)
		ct.Status = domain.Todo
		if parent.Valid {
			ct.ParentKey = parent.String
		}
		rep.NextUp = append(rep.NextUp, ct)
	}
	nRows.Close()
	if err := nRows.Err(); err != nil {
		return rep, err
	}

	// Counts.
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tickets WHERE status = 'backlog'`+projClause, projArg...).
		Scan(&rep.BacklogN); err != nil {
		return rep, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tickets WHERE status = 'done'`+projClause, projArg...).
		Scan(&rep.DoneN); err != nil {
		return rep, err
	}

	return rep, nil
}
