package mcptools

import (
	"fmt"
	"strings"
	"time"

	"github.com/colossus21/ticketd/internal/domain"
	"github.com/colossus21/ticketd/internal/store"
)

// displayTime renders a stored RFC3339 UTC instant in local time, minute
// precision, for human/agent reading.
func displayTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// displayDate renders date only.
func displayDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02")
}

// Ticket renders the canonical full ticket view (§4.4). If existed is true a
// duplicate-create note is prepended.
func Ticket(t domain.Ticket, existed bool) string {
	var b strings.Builder
	if existed {
		b.WriteString("Note: an open ticket with this exact title already exists; returning it instead of creating a duplicate.\n\n")
	}
	fmt.Fprintf(&b, "# %s: %s\n", t.Key, t.Title)
	fmt.Fprintf(&b, "status: %s · priority: %s · project: %s\n", t.Status, t.Priority, t.Project)

	meta := []string{}
	if t.ParentKey != "" {
		meta = append(meta, "parent: "+t.ParentKey)
	}
	if len(t.Labels) > 0 {
		meta = append(meta, "labels: "+strings.Join(t.Labels, ", "))
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, " · ") + "\n")
	}
	fmt.Fprintf(&b, "created: %s · updated: %s\n", displayDate(t.CreatedAt), displayDate(t.UpdatedAt))

	if strings.TrimSpace(t.Description) != "" {
		b.WriteString("\n" + strings.TrimSpace(t.Description) + "\n")
	}

	if len(t.Subtasks) > 0 {
		b.WriteString("\n## Subtasks\n")
		for _, st := range t.Subtasks {
			box := " "
			if st.Status == domain.Done {
				box = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s: %s (%s)\n", box, st.Key, st.Title, st.Status)
		}
	}

	if len(t.Links) > 0 {
		b.WriteString("\n## Links\n")
		for _, ln := range t.Links {
			fmt.Fprintf(&b, "- %s %s: %s\n", ln.Kind, ln.ToKey, ln.ToTitle)
		}
	}

	fmt.Fprintf(&b, "\n## Worklog (%d comment%s)\n", len(t.Comments), plural(len(t.Comments)))
	if len(t.Comments) == 0 {
		b.WriteString("_No comments yet._\n")
	}
	for _, c := range t.Comments {
		fmt.Fprintf(&b, "**[%s] %s** — %s\n", displayTime(c.CreatedAt), c.Author, strings.TrimSpace(c.Body))
	}
	return b.String()
}

// ticketHeader renders just the title/status line block, used to confirm an
// update without dumping the whole worklog.
func ticketHeader(t domain.Ticket) string {
	return fmt.Sprintf("# %s: %s\nstatus: %s · priority: %s · project: %s",
		t.Key, t.Title, t.Status, t.Priority, t.Project)
}

// commentConfirmation is the one-line acknowledgement after add_comment.
func commentConfirmation(key string, count int) string {
	return fmt.Sprintf("Comment added to %s (%d comment%s total).", key, count, plural(count))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// SearchResults renders the compact match list (§4.5).
func SearchResults(query string, results []store.SearchResult) string {
	var b strings.Builder
	if query != "" {
		fmt.Fprintf(&b, "%d result%s for %q:\n", len(results), plural(len(results)), query)
	} else {
		fmt.Fprintf(&b, "%d result%s:\n", len(results), plural(len(results)))
	}
	if len(results) == 0 {
		b.WriteString("_Nothing matched. Consider create_ticket._\n")
		return b.String()
	}
	for _, r := range results {
		fmt.Fprintf(&b, "- %s [%s, %s] %s — %s\n",
			r.Key, r.Status, r.Priority, r.Title, matchDescription(r))
	}
	return b.String()
}

func matchDescription(r store.SearchResult) string {
	switch {
	case r.MatchedTitle && r.MatchedBody:
		return fmt.Sprintf("matched title + comment, %d comment%s", r.CommentCount, plural(r.CommentCount))
	case r.MatchedBody && r.Snippet != "":
		return fmt.Sprintf("matched comment: \"%s\"", r.Snippet)
	case r.MatchedBody:
		return fmt.Sprintf("matched comment, %d comment%s", r.CommentCount, plural(r.CommentCount))
	case r.MatchedTitle:
		return "matched title"
	default:
		return fmt.Sprintf("%d comment%s", r.CommentCount, plural(r.CommentCount))
	}
}

// RenderContextReport renders the working-state report dated today (local).
// Convenience for callers (CLI) that don't carry their own clock.
func RenderContextReport(rep store.ContextReport) string {
	return Context(rep, now().Local().Format("2006-01-02"))
}

// Context renders the working-state report (§4.6).
func Context(rep store.ContextReport, today string) string {
	var b strings.Builder
	project := rep.Project
	if project == "" {
		project = "all projects"
	}
	fmt.Fprintf(&b, "# Context: %s — %s\n", project, today)

	fmt.Fprintf(&b, "\n## In progress (%d)\n", len(rep.InProgress))
	if len(rep.InProgress) == 0 {
		b.WriteString("_Nothing in progress._\n")
	}
	for _, ct := range rep.InProgress {
		stale := ""
		if ct.Stale {
			stale = fmt.Sprintf(" ⚠ STALE — untouched %d days", ct.StaleDays)
		}
		fmt.Fprintf(&b, "### %s: %s [%s]%s\n", ct.Key, ct.Title, ct.Priority, stale)
		if ct.LastComment != "" {
			fmt.Fprintf(&b, "Last activity %s:\n> %s\n", displayTime(parse(ct.LastActivity)), firstLine(ct.LastComment))
		} else {
			b.WriteString("_No worklog yet._\n")
		}
	}

	fmt.Fprintf(&b, "\n## Blocked (%d)\n", len(rep.Blocked))
	if len(rep.Blocked) == 0 {
		b.WriteString("_Nothing blocked._\n")
	}
	for _, bt := range rep.Blocked {
		if len(bt.BlockedOn) == 0 {
			fmt.Fprintf(&b, "- %s: %s\n", bt.Key, bt.Title)
			continue
		}
		for _, on := range bt.BlockedOn {
			fmt.Fprintf(&b, "- %s → blocked on %s (%s)\n", bt.Key, on.ToKey, on.ToTitle)
		}
	}

	b.WriteString("\n## Next up (top 5 todo by priority)\n")
	if len(rep.NextUp) == 0 {
		b.WriteString("_Todo queue is empty._\n")
	}
	for i, ct := range rep.NextUp {
		suffix := ""
		if ct.ParentKey != "" {
			suffix = fmt.Sprintf("  (subtask of %s)", ct.ParentKey)
		}
		fmt.Fprintf(&b, "%d. %s [%s] %s%s\n", i+1, ct.Key, ct.Priority, ct.Title, suffix)
	}

	fmt.Fprintf(&b, "\n(%d ticket%s in backlog · %d done · use search_tickets to dig deeper)\n",
		rep.BacklogN, plural(rep.BacklogN), rep.DoneN)
	return b.String()
}

func parse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// firstLine collapses a multi-line comment to its first non-empty line for
// the context excerpt.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}
