package mcptools

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rafiulalam/ticketd/internal/domain"
	"github.com/rafiulalam/ticketd/internal/store"
)

var update = flag.Bool("update", false, "update golden files")

// Render uses local time; pin it to UTC so goldens are machine-independent.
func init() { time.Local = time.UTC }

func ts(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// assertGolden compares got against testdata/<name>.golden, rewriting it when
// -update is passed. The format is agent-facing API: diffs are reviewed.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func sampleTicket() domain.Ticket {
	pid := int64(40)
	return domain.Ticket{
		Key: "T-42", Project: "voice-assistant", Title: "Add Zigbee sensor MCP bridge",
		Status: domain.InProgress, Priority: domain.High, ParentID: &pid, ParentKey: "T-40",
		Labels:      []string{"mqtt", "home-assistant"},
		Description: "Bridge zigbee2mqtt sensor topics into the assistant's MCP server.",
		CreatedAt:   ts("2026-06-10T00:00:00Z"), UpdatedAt: ts("2026-06-11T00:00:00Z"),
		Subtasks: []domain.Subtask{
			{Key: "T-43", Title: "Define sensor reading schema", Status: domain.Done},
			{Key: "T-44", Title: "Subscribe to zigbee2mqtt/+", Status: domain.InProgress},
		},
		Links: []domain.Link{
			{Kind: domain.LinkBlocks, ToKey: "T-39", ToTitle: "Voice query routing"},
			{Kind: domain.LinkRelates, ToKey: "T-21", ToTitle: "Home Assistant token setup"},
		},
		Comments: []domain.Comment{
			{Author: "agent", Body: "Plan: paho-mqtt client, cache last reading per device.", CreatedAt: ts("2026-06-10T18:30:00Z")},
			{Author: "agent", Body: "BLOCKED: HA token lacks scope. See T-21.", CreatedAt: ts("2026-06-11T10:02:00Z")},
		},
	}
}

func TestRenderTicketGolden(t *testing.T) {
	assertGolden(t, "ticket", Ticket(sampleTicket(), false))
}

func TestRenderSearchGolden(t *testing.T) {
	results := []store.SearchResult{
		{Key: "T-42", Title: "Add Zigbee sensor MCP bridge", Status: domain.InProgress, Priority: domain.High, MatchedTitle: true, CommentCount: 2},
		{Key: "T-21", Title: "Home Assistant token setup", Status: domain.Done, Priority: domain.Normal, MatchedBody: true, Snippet: "…zigbee2mqtt auth…"},
		{Key: "T-19", Title: "Zigbee direct serial", Status: domain.WontDo, Priority: domain.Low, MatchedTitle: true},
	}
	assertGolden(t, "search", SearchResults("zigbee", results))
}

func TestRenderContextGolden(t *testing.T) {
	rep := store.ContextReport{
		Project: "voice-assistant",
		InProgress: []store.ContextTicket{{
			Key: "T-42", Title: "Add Zigbee sensor MCP bridge", Priority: domain.High,
			LastActivity: "2026-06-11T10:02:00Z", LastComment: "BLOCKED: HA token lacks scope. See T-21.",
		}},
		Blocked: []store.BlockedTicket{{
			Key: "T-42", Title: "Add Zigbee sensor MCP bridge",
			BlockedOn: []domain.Link{{Kind: domain.LinkBlocks, ToKey: "T-21", ToTitle: "Home Assistant token setup"}},
		}},
		NextUp: []store.ContextTicket{
			{Key: "T-44", Title: "Subscribe to zigbee2mqtt/+", Priority: domain.High, ParentKey: "T-42"},
			{Key: "T-45", Title: "Expose get_sensor MCP tool", Priority: domain.Normal},
		},
		BacklogN: 12, DoneN: 27,
	}
	assertGolden(t, "context", Context(rep, "2026-06-11"))
}

func TestRenderEmptyWorklog(t *testing.T) {
	tk := domain.Ticket{Key: "T-1", Project: "default", Title: "Fresh", Status: domain.Todo, Priority: domain.Normal, Labels: []string{}}
	out := Ticket(tk, false)
	if !contains(out, "Worklog (0 comments)") || !contains(out, "_No comments yet._") {
		t.Errorf("empty worklog render wrong:\n%s", out)
	}
}

func TestRenderDuplicateNote(t *testing.T) {
	tk := domain.Ticket{Key: "T-1", Project: "default", Title: "Dup", Status: domain.Todo, Priority: domain.Normal, Labels: []string{}}
	out := Ticket(tk, true)
	if !contains(out, "already exists") {
		t.Errorf("duplicate note missing:\n%s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
