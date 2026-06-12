package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colossus21/ticketd/internal/domain"
	"github.com/colossus21/ticketd/internal/store"
)

func TestBoardRenders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.CreateTicket(ctx, store.CreateParams{Title: "Add Zigbee bridge"})
	ip := domain.InProgress
	st.UpdateTicket(ctx, store.UpdateParams{Key: "T-1", Status: &ip})

	req := httptest.NewRequest(http.MethodGet, "/board", nil)
	rec := httptest.NewRecorder()
	BoardHandler(st).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Add Zigbee bridge") {
		t.Fatalf("board missing ticket title:\n%s", body)
	}
	if !strings.Contains(body, "in_progress") {
		t.Fatalf("board missing in_progress column:\n%s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("unexpected content-type %q", ct)
	}
}
