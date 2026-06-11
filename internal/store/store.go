// Package store owns all SQL and is the only package that imports the SQLite
// driver. It exposes domain types; callers never see *sql.Row.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rafiulalam/ticketd/internal/domain"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a ticket key does not resolve.
var ErrNotFound = errors.New("not found")

// Store wraps a SQLite connection pool.
type Store struct {
	db *sql.DB
}

// now is overridable in tests for deterministic timestamps.
var now = func() time.Time { return time.Now().UTC() }

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// Open opens (creating if needed) the database at path, applies pragmas and
// migrations, and returns a ready Store.
func Open(path string) (*Store, error) {
	// Connection pragmas applied on every new connection via the DSN.
	// _txlock=immediate makes BeginTx issue BEGIN IMMEDIATE so writers take
	// the write lock upfront and queue on busy_timeout instead of deadlocking
	// on a read→write upgrade.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// CreateParams carries the inputs for creating a ticket.
type CreateParams struct {
	Title       string
	Description string
	Project     string
	ParentKey   string
	Priority    domain.Priority
	Labels      []string
}

// CreateTicket inserts a new ticket, allocating its key atomically from the
// project counter. If an open (non-terminal) ticket in the same project has a
// byte-identical title, that ticket is returned with existed=true and nothing
// is inserted — duplicates poison get_context, and agents retry.
func (s *Store) CreateTicket(ctx context.Context, p CreateParams) (t domain.Ticket, existed bool, err error) {
	project := p.Project
	if project == "" {
		project = "default"
	}
	if strings.TrimSpace(p.Title) == "" {
		return domain.Ticket{}, false, errors.New("title must not be empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Ticket{}, false, err
	}
	defer tx.Rollback()

	// Idempotency check against open tickets with the same exact title.
	var existingKey string
	err = tx.QueryRowContext(ctx,
		`SELECT key FROM tickets
		 WHERE project = ? AND title = ? AND status NOT IN ('done','wont_do')
		 ORDER BY id LIMIT 1`,
		project, p.Title).Scan(&existingKey)
	if err == nil {
		// Found an open duplicate; return it.
		full, gerr := getTicketTx(ctx, tx, existingKey)
		if gerr != nil {
			return domain.Ticket{}, false, gerr
		}
		if cerr := tx.Commit(); cerr != nil {
			return domain.Ticket{}, false, cerr
		}
		return full, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Ticket{}, false, err
	}

	// Allocate the next key from the counter.
	var seq int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO counters(project, next) VALUES (?, 2)
		 ON CONFLICT(project) DO UPDATE SET next = next + 1
		 RETURNING next - 1`, project).Scan(&seq)
	if err != nil {
		return domain.Ticket{}, false, fmt.Errorf("allocate key: %w", err)
	}
	key := fmt.Sprintf("T-%d", seq)

	var parentID *int64
	if p.ParentKey != "" {
		id, lerr := lookupID(ctx, tx, p.ParentKey)
		if lerr != nil {
			return domain.Ticket{}, false, fmt.Errorf("parent %s: %w", p.ParentKey, lerr)
		}
		parentID = &id
	}

	labels := p.Labels
	if labels == nil {
		labels = []string{}
	}
	labelsJSON, _ := json.Marshal(labels)
	ts := rfc3339(now())

	res, err := tx.ExecContext(ctx,
		`INSERT INTO tickets(key, project, title, description, status, priority, parent_id, labels, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'todo', ?, ?, ?, ?, ?)`,
		key, project, p.Title, p.Description, int(p.Priority), parentID, string(labelsJSON), ts, ts)
	if err != nil {
		return domain.Ticket{}, false, err
	}
	id, _ := res.LastInsertId()

	full, err := getTicketTx(ctx, tx, key)
	if err != nil {
		return domain.Ticket{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Ticket{}, false, err
	}
	_ = id
	return full, false, nil
}

// UpdateParams carries a partial update. Nil pointers mean "leave unchanged".
type UpdateParams struct {
	Key         string
	Status      *domain.Status
	Priority    *domain.Priority
	Title       *string
	Description *string
	Labels      *[]string
	LinkBlocks  string
	LinkRelates string
}

// UpdateTicket applies a partial update, validating status transitions through
// the FSM and recording any links. Returns the refreshed ticket and a short
// human-readable summary of what changed.
func (s *Store) UpdateTicket(ctx context.Context, p UpdateParams) (domain.Ticket, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Ticket{}, "", err
	}
	defer tx.Rollback()

	cur, err := getTicketTx(ctx, tx, p.Key)
	if err != nil {
		return domain.Ticket{}, "", err
	}

	var changes []string
	sets := []string{}
	args := []any{}

	if p.Status != nil && *p.Status != cur.Status {
		if err := domain.ValidateTransition(cur.Key, cur.Status, *p.Status); err != nil {
			return domain.Ticket{}, "", err
		}
		sets = append(sets, "status = ?")
		args = append(args, string(*p.Status))
		changes = append(changes, fmt.Sprintf("status %s→%s", cur.Status, *p.Status))
	}
	if p.Priority != nil && *p.Priority != cur.Priority {
		sets = append(sets, "priority = ?")
		args = append(args, int(*p.Priority))
		changes = append(changes, fmt.Sprintf("priority %s→%s", cur.Priority, *p.Priority))
	}
	if p.Title != nil && *p.Title != cur.Title {
		if strings.TrimSpace(*p.Title) == "" {
			return domain.Ticket{}, "", errors.New("title must not be empty")
		}
		sets = append(sets, "title = ?")
		args = append(args, *p.Title)
		changes = append(changes, "title")
	}
	if p.Description != nil && *p.Description != cur.Description {
		sets = append(sets, "description = ?")
		args = append(args, *p.Description)
		changes = append(changes, "description")
	}
	if p.Labels != nil {
		labelsJSON, _ := json.Marshal(*p.Labels)
		sets = append(sets, "labels = ?")
		args = append(args, string(labelsJSON))
		changes = append(changes, "labels")
	}

	if len(sets) > 0 {
		sets = append(sets, "updated_at = ?")
		args = append(args, rfc3339(now()))
		args = append(args, cur.ID)
		q := "UPDATE tickets SET " + strings.Join(sets, ", ") + " WHERE id = ?"
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return domain.Ticket{}, "", err
		}
	}

	if p.LinkBlocks != "" {
		if err := addLink(ctx, tx, cur.ID, p.LinkBlocks, domain.LinkBlocks); err != nil {
			return domain.Ticket{}, "", err
		}
		changes = append(changes, "blocks "+p.LinkBlocks)
	}
	if p.LinkRelates != "" {
		if err := addLink(ctx, tx, cur.ID, p.LinkRelates, domain.LinkRelates); err != nil {
			return domain.Ticket{}, "", err
		}
		changes = append(changes, "relates "+p.LinkRelates)
	}

	full, err := getTicketTx(ctx, tx, p.Key)
	if err != nil {
		return domain.Ticket{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return domain.Ticket{}, "", err
	}

	summary := "no changes"
	if len(changes) > 0 {
		summary = "updated: " + strings.Join(changes, ", ")
	}
	return full, summary, nil
}

// AddComment appends a worklog entry and touches the ticket's updated_at.
// Returns the new total comment count.
func (s *Store) AddComment(ctx context.Context, key, author, body string) (int, error) {
	if strings.TrimSpace(body) == "" {
		return 0, errors.New("comment body must not be empty")
	}
	if author == "" {
		author = "agent"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	id, err := lookupID(ctx, tx, key)
	if err != nil {
		return 0, err
	}
	ts := rfc3339(now())
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO comments(ticket_id, author, body, created_at) VALUES (?, ?, ?, ?)`,
		id, author, body, ts); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tickets SET updated_at = ? WHERE id = ?`, ts, id); err != nil {
		return 0, err
	}
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM comments WHERE ticket_id = ?`, id).Scan(&count); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// GetTicketFull returns a ticket with its comments, subtasks, and links.
func (s *Store) GetTicketFull(ctx context.Context, key string) (domain.Ticket, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.Ticket{}, err
	}
	defer tx.Rollback()
	return getTicketTx(ctx, tx, key)
}

// --- internal helpers, all operating within a transaction ---

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func lookupID(ctx context.Context, q queryer, key string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM tickets WHERE key = ?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("ticket %s: %w", key, ErrNotFound)
	}
	return id, err
}

func addLink(ctx context.Context, q queryer, fromID int64, toKey string, kind domain.LinkKind) error {
	toID, err := lookupID(ctx, q, toKey)
	if err != nil {
		return err
	}
	if toID == fromID {
		return fmt.Errorf("a ticket cannot link to itself")
	}
	_, err = q.ExecContext(ctx,
		`INSERT OR IGNORE INTO links(from_id, to_id, kind) VALUES (?, ?, ?)`,
		fromID, toID, string(kind))
	return err
}

// getTicketTx loads a ticket and all its associated data within tx.
func getTicketTx(ctx context.Context, q queryer, key string) (domain.Ticket, error) {
	var t domain.Ticket
	var parentID sql.NullInt64
	var labelsJSON, createdAt, updatedAt, status string
	var priority int

	err := q.QueryRowContext(ctx,
		`SELECT id, key, project, title, description, status, priority, parent_id, labels, created_at, updated_at
		 FROM tickets WHERE key = ?`, key).
		Scan(&t.ID, &t.Key, &t.Project, &t.Title, &t.Description, &status, &priority,
			&parentID, &labelsJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Ticket{}, fmt.Errorf("ticket %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return domain.Ticket{}, err
	}
	t.Status = domain.Status(status)
	t.Priority = domain.Priority(priority)
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	_ = json.Unmarshal([]byte(labelsJSON), &t.Labels)
	if t.Labels == nil {
		t.Labels = []string{}
	}

	if parentID.Valid {
		t.ParentID = &parentID.Int64
		_ = q.QueryRowContext(ctx, `SELECT key FROM tickets WHERE id = ?`, parentID.Int64).Scan(&t.ParentKey)
	}

	// Subtasks
	rows, err := q.QueryContext(ctx,
		`SELECT key, title, status FROM tickets WHERE parent_id = ? ORDER BY id`, t.ID)
	if err != nil {
		return domain.Ticket{}, err
	}
	for rows.Next() {
		var st domain.Subtask
		var s string
		if err := rows.Scan(&st.Key, &st.Title, &s); err != nil {
			rows.Close()
			return domain.Ticket{}, err
		}
		st.Status = domain.Status(s)
		t.Subtasks = append(t.Subtasks, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.Ticket{}, err
	}

	// Links (outgoing)
	lrows, err := q.QueryContext(ctx,
		`SELECT l.kind, tt.key, tt.title
		 FROM links l JOIN tickets tt ON tt.id = l.to_id
		 WHERE l.from_id = ? ORDER BY l.kind, tt.id`, t.ID)
	if err != nil {
		return domain.Ticket{}, err
	}
	for lrows.Next() {
		var ln domain.Link
		var kind string
		if err := lrows.Scan(&kind, &ln.ToKey, &ln.ToTitle); err != nil {
			lrows.Close()
			return domain.Ticket{}, err
		}
		ln.Kind = domain.LinkKind(kind)
		t.Links = append(t.Links, ln)
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return domain.Ticket{}, err
	}

	// Comments
	crows, err := q.QueryContext(ctx,
		`SELECT id, ticket_id, author, body, created_at FROM comments
		 WHERE ticket_id = ? ORDER BY created_at, id`, t.ID)
	if err != nil {
		return domain.Ticket{}, err
	}
	for crows.Next() {
		var c domain.Comment
		var ca string
		if err := crows.Scan(&c.ID, &c.TicketID, &c.Author, &c.Body, &ca); err != nil {
			crows.Close()
			return domain.Ticket{}, err
		}
		c.CreatedAt = parseTime(ca)
		t.Comments = append(t.Comments, c)
	}
	crows.Close()
	return t, crows.Err()
}
