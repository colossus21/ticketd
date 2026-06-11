package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rafiulalam/ticketd/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tickets.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCreateAndGet(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	tk, existed, err := st.CreateTicket(ctx, CreateParams{
		Title:       "Add Zigbee bridge",
		Description: "Bridge sensors",
		Project:     "voice",
		Priority:    domain.High,
		Labels:      []string{"mqtt"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if existed {
		t.Fatal("first create should not report existed")
	}
	if tk.Key != "T-1" {
		t.Fatalf("expected key T-1, got %s", tk.Key)
	}
	if tk.Status != domain.Todo {
		t.Fatalf("new ticket should be todo, got %s", tk.Status)
	}

	got, err := st.GetTicketFull(ctx, "T-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Add Zigbee bridge" || got.Priority != domain.High {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "mqtt" {
		t.Fatalf("labels mismatch: %v", got.Labels)
	}
}

func TestKeySequencePerProject(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	a, _, _ := st.CreateTicket(ctx, CreateParams{Title: "one", Project: "p"})
	b, _, _ := st.CreateTicket(ctx, CreateParams{Title: "two", Project: "p"})
	if a.Key != "T-1" || b.Key != "T-2" {
		t.Fatalf("expected T-1, T-2; got %s, %s", a.Key, b.Key)
	}
}

func TestIdempotentCreateOnExactTitle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	first, _, _ := st.CreateTicket(ctx, CreateParams{Title: "Refactor auth", Project: "x"})
	second, existed, err := st.CreateTicket(ctx, CreateParams{Title: "Refactor auth", Project: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatal("duplicate title should return existed=true")
	}
	if second.Key != first.Key {
		t.Fatalf("duplicate should return same key, got %s vs %s", second.Key, first.Key)
	}

	// Once a same-title ticket is terminal (wont_do), a fresh create is allowed.
	wont := domain.WontDo
	if _, _, err := st.UpdateTicket(ctx, UpdateParams{Key: first.Key, Status: &wont}); err != nil {
		t.Fatalf("todo->wont_do should be allowed: %v", err)
	}
	third, existed, err := st.CreateTicket(ctx, CreateParams{Title: "Refactor auth", Project: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if existed || third.Key == first.Key {
		t.Fatalf("terminal same-title ticket should not block a new create; got existed=%v key=%s", existed, third.Key)
	}
}

func TestFSMValidationOnUpdate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateTicket(ctx, CreateParams{Title: "work"})

	review := domain.InReview
	_, _, err := st.UpdateTicket(ctx, UpdateParams{Key: "T-1", Status: &review})
	if err == nil {
		t.Fatal("todo->in_review should be rejected")
	}
	if !strings.Contains(err.Error(), "legal transitions") {
		t.Fatalf("error should list legal moves: %v", err)
	}

	ip := domain.InProgress
	if _, _, err := st.UpdateTicket(ctx, UpdateParams{Key: "T-1", Status: &ip}); err != nil {
		t.Fatalf("todo->in_progress should be allowed: %v", err)
	}
}

func TestAddCommentTouchesUpdatedAt(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	tk, _, _ := st.CreateTicket(ctx, CreateParams{Title: "work"})

	n, err := st.AddComment(ctx, "T-1", "agent", "first note")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 comment, got %d", n)
	}
	got, _ := st.GetTicketFull(ctx, "T-1")
	if !got.UpdatedAt.After(tk.UpdatedAt) && !got.UpdatedAt.Equal(tk.UpdatedAt) {
		t.Fatal("updated_at should advance or stay equal")
	}
	if len(got.Comments) != 1 || got.Comments[0].Body != "first note" {
		t.Fatalf("comment not stored: %+v", got.Comments)
	}
}

func TestSubtasksAndLinks(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	parent, _, _ := st.CreateTicket(ctx, CreateParams{Title: "parent"})
	st.CreateTicket(ctx, CreateParams{Title: "child", ParentKey: parent.Key})
	other, _, _ := st.CreateTicket(ctx, CreateParams{Title: "other"})

	_, _, err := st.UpdateTicket(ctx, UpdateParams{Key: parent.Key, LinkBlocks: other.Key})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetTicketFull(ctx, parent.Key)
	if len(got.Subtasks) != 1 || got.Subtasks[0].Title != "child" {
		t.Fatalf("subtask missing: %+v", got.Subtasks)
	}
	if len(got.Links) != 1 || got.Links[0].Kind != domain.LinkBlocks || got.Links[0].ToKey != other.Key {
		t.Fatalf("link missing: %+v", got.Links)
	}
}

func TestSearchTitleAndComment(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateTicket(ctx, CreateParams{Title: "Add Zigbee sensor bridge"})
	st.CreateTicket(ctx, CreateParams{Title: "Unrelated thing"})
	st.AddComment(ctx, "T-2", "agent", "this mentions zigbee in passing")

	res, err := st.Search(ctx, SearchParams{Query: "zigbee"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results for zigbee, got %d: %+v", len(res), res)
	}

	// Query with FTS metacharacters must not error.
	if _, err := st.Search(ctx, SearchParams{Query: `zigbee OR "drop`}); err != nil {
		t.Fatalf("sanitized query should not error: %v", err)
	}
}

func TestSearchStatusFilterNoQuery(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateTicket(ctx, CreateParams{Title: "a"})
	st.CreateTicket(ctx, CreateParams{Title: "b"})
	ip := domain.InProgress
	st.UpdateTicket(ctx, UpdateParams{Key: "T-1", Status: &ip})

	res, err := st.Search(ctx, SearchParams{Status: "in_progress"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Key != "T-1" {
		t.Fatalf("status filter failed: %+v", res)
	}
}

func TestContextReport(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateTicket(ctx, CreateParams{Title: "active", Priority: domain.High})
	ip := domain.InProgress
	st.UpdateTicket(ctx, UpdateParams{Key: "T-1", Status: &ip})
	st.AddComment(ctx, "T-1", "agent", "BLOCKED-ish note about progress")

	st.CreateTicket(ctx, CreateParams{Title: "queued", Priority: domain.Normal})

	rep, err := st.ContextReport(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.InProgress) != 1 || rep.InProgress[0].Key != "T-1" {
		t.Fatalf("in-progress wrong: %+v", rep.InProgress)
	}
	if rep.InProgress[0].LastComment == "" {
		t.Fatal("in-progress ticket should carry its last comment")
	}
	if len(rep.NextUp) != 1 || rep.NextUp[0].Key != "T-2" {
		t.Fatalf("next-up wrong: %+v", rep.NextUp)
	}
}

func TestConcurrentComments(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateTicket(ctx, CreateParams{Title: "busy"})

	var wg sync.WaitGroup
	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.AddComment(ctx, "T-1", "agent", "concurrent note")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent comment failed: %v", err)
		}
	}
	got, _ := st.GetTicketFull(ctx, "T-1")
	if len(got.Comments) != n {
		t.Fatalf("expected %d comments, got %d", n, len(got.Comments))
	}
}

func TestNotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetTicketFull(context.Background(), "T-999")
	if err == nil {
		t.Fatal("expected not-found error")
	}
}
