// Package domain holds the core ticket types and the status state machine.
// It knows nothing about MCP, SQLite, or any transport — it is pure data and
// rules, imported by store, cli, and mcptools alike.
package domain

import (
	"fmt"
	"strings"
	"time"
)

// Priority is an ordered severity; lower sorts first.
type Priority int

const (
	Critical Priority = iota // 0
	High                     // 1
	Normal                   // 2
	Low                      // 3
)

var priorityNames = map[Priority]string{
	Critical: "critical",
	High:     "high",
	Normal:   "normal",
	Low:      "low",
}

var priorityByName = map[string]Priority{
	"critical": Critical,
	"high":     High,
	"normal":   Normal,
	"low":      Low,
}

func (p Priority) String() string {
	if name, ok := priorityNames[p]; ok {
		return name
	}
	return "normal"
}

// ParsePriority maps a name to a Priority. Empty defaults to Normal.
func ParsePriority(s string) (Priority, error) {
	if s == "" {
		return Normal, nil
	}
	if p, ok := priorityByName[strings.ToLower(s)]; ok {
		return p, nil
	}
	return Normal, fmt.Errorf("unknown priority %q: use one of critical, high, normal, low", s)
}

// ClaimTTL is how long a soft claim stays active without renewal. After this,
// the claim is considered stale and another agent may take the ticket — so a
// crashed or abandoned agent never blocks work indefinitely.
const ClaimTTL = 30 * time.Minute

// ClaimActiveAt reports whether the claim is held and not yet expired at t.
func (k Ticket) ClaimActiveAt(t time.Time) bool {
	return k.ClaimedBy != "" && !k.ClaimedAt.IsZero() && t.Sub(k.ClaimedAt) < ClaimTTL
}

// LinkKind enumerates the relationship types between tickets.
type LinkKind string

const (
	LinkBlocks  LinkKind = "blocks"
	LinkRelates LinkKind = "relates"
)

// Ticket is the central record. Labels live as a decoded slice here even
// though the store persists them as a JSON column.
type Ticket struct {
	ID          int64
	Key         string
	Project     string
	Title       string
	Description string
	Status      Status
	Priority    Priority
	ParentID    *int64
	ParentKey   string // populated on read for rendering convenience
	Labels      []string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// ClaimedBy is the advisory owner (agent id); empty when unclaimed.
	// ClaimedAt drives TTL expiry of the soft claim.
	ClaimedBy string
	ClaimedAt time.Time

	// Populated only by GetTicketFull / context queries.
	Comments []Comment
	Subtasks []Subtask
	Links    []Link

	// CommentCount is set by lightweight list queries (e.g. the board) that
	// do not load the full Comments slice.
	CommentCount int
}

// Comment is an append-only worklog entry. There is no edit or delete.
type Comment struct {
	ID        int64
	TicketID  int64
	Author    string
	Body      string
	CreatedAt time.Time
}

// Subtask is a lightweight view of a child ticket for rendering parents.
type Subtask struct {
	Key    string
	Title  string
	Status Status
}

// Link is a directed relationship to another ticket, with the far ticket's
// key and title resolved for rendering.
type Link struct {
	Kind    LinkKind
	ToKey   string
	ToTitle string
}
