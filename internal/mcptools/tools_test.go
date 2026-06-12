package mcptools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/colossus21/ticketd/internal/store"
)

// newClientServer wires a server (backed by a temp-file store) to an in-process
// client over the SDK's in-memory transport, and returns a connected session.
func newClientServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tickets.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := mcp.NewServer(&mcp.Implementation{Name: "tickets", Version: "test"}, nil)
	Register(srv, st)

	clientT, serverT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestEndToEndFlow(t *testing.T) {
	cs := newClientServer(t)

	// Create.
	out, isErr := callText(t, cs, "create_ticket", CreateTicketInput{
		Title:    "Add Zigbee sensor MCP bridge",
		Priority: "high",
		Project:  "voice",
	})
	if isErr {
		t.Fatalf("create returned error: %s", out)
	}
	if !strings.Contains(out, "T-1") || !strings.Contains(out, "priority: high") {
		t.Fatalf("unexpected create output:\n%s", out)
	}

	// Move to in_progress.
	out, isErr = callText(t, cs, "update_ticket", UpdateTicketInput{Key: "T-1", Status: "in_progress"})
	if isErr {
		t.Fatalf("update error: %s", out)
	}
	if !strings.Contains(out, "status in_progress") && !strings.Contains(out, "in_progress") {
		t.Fatalf("update summary missing transition:\n%s", out)
	}

	// Illegal transition surfaces as a readable, non-protocol error.
	out, isErr = callText(t, cs, "update_ticket", UpdateTicketInput{Key: "T-1", Status: "backlog"})
	if !isErr {
		t.Fatalf("expected illegal transition to be flagged as error; got:\n%s", out)
	}
	if !strings.Contains(out, "legal transitions") {
		t.Fatalf("illegal transition should list legal moves:\n%s", out)
	}

	// Comment.
	out, _ = callText(t, cs, "add_comment", AddCommentInput{Key: "T-1", Body: "Plan: subscribe to zigbee2mqtt/+"})
	if !strings.Contains(out, "Comment added to T-1 (1 comment total)") {
		t.Fatalf("unexpected comment confirmation:\n%s", out)
	}

	// get_context shows the in-progress ticket with its last comment.
	out, _ = callText(t, cs, "get_context", GetContextInput{Project: "voice"})
	if !strings.Contains(out, "In progress (1)") || !strings.Contains(out, "zigbee2mqtt") {
		t.Fatalf("context missing in-progress excerpt:\n%s", out)
	}

	// Search finds it.
	out, _ = callText(t, cs, "search_tickets", SearchTicketsInput{Query: "zigbee"})
	if !strings.Contains(out, "T-1") {
		t.Fatalf("search did not find ticket:\n%s", out)
	}

	// get_ticket renders the full worklog.
	out, _ = callText(t, cs, "get_ticket", GetTicketInput{Key: "T-1"})
	if !strings.Contains(out, "Worklog (1 comment)") {
		t.Fatalf("get_ticket missing worklog:\n%s", out)
	}
}

func TestNotFoundIsReadableError(t *testing.T) {
	cs := newClientServer(t)
	out, isErr := callText(t, cs, "get_ticket", GetTicketInput{Key: "T-404"})
	if !isErr {
		t.Fatal("expected error result for missing ticket")
	}
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found message, got: %s", out)
	}
}
