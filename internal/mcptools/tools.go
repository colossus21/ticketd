// Package mcptools is the thin glue between the MCP server and the store. It
// registers the six tools, converts inputs to store calls, and renders results
// to Markdown. Validation errors are returned as readable tool-result text so
// the model can read and self-correct; only infrastructure failures surface as
// Go errors.
package mcptools

import (
	"context"
	"time"

	"github.com/colossus21/ticketd/internal/domain"
	"github.com/colossus21/ticketd/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// now is overridable in tests for deterministic context dates.
var now = func() time.Time { return time.Now() }

// Register wires all six tools onto the server.
func Register(s *mcp.Server, st *store.Store) {
	mcp.AddTool(s, &mcp.Tool{Name: "create_ticket", Description: createDesc}, handleCreate(st))
	mcp.AddTool(s, &mcp.Tool{Name: "update_ticket", Description: updateDesc}, handleUpdate(st))
	mcp.AddTool(s, &mcp.Tool{Name: "add_comment", Description: commentDesc}, handleComment(st))
	mcp.AddTool(s, &mcp.Tool{Name: "get_ticket", Description: getDesc}, handleGet(st))
	mcp.AddTool(s, &mcp.Tool{Name: "search_tickets", Description: searchDesc}, handleSearch(st))
	mcp.AddTool(s, &mcp.Tool{Name: "get_context", Description: contextDesc}, handleContext(st))
}

// textResult builds a successful Markdown tool result.
func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// errResult builds an error-flagged tool result carrying a readable message.
func errResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}
}

// --- tool descriptions (verbatim prompt engineering from the design doc) ---

const createDesc = "Create a ticket. Use when starting any non-trivial or multi-step piece of work. Returns the ticket key (e.g. T-42); reference it in commit messages and comments. For large work, create a parent ticket then subtasks via parent_key."

const updateDesc = "Update a ticket's status, priority, title, description, or labels. Status transitions are validated; on rejection the error lists the legal moves. When moving to blocked, immediately add_comment explaining the blocker. When moving to done, immediately add_comment summarizing the outcome."

const commentDesc = "Append a worklog entry to a ticket. Comment whenever you: form a plan, make a non-obvious decision, touch files worth recording, try an approach that fails, hit a blocker, or finish. Comments are append-only and are the memory a future session resumes from — write for a reader with zero context."

const getDesc = "Fetch a ticket with its full worklog, subtasks, and links in one call. Call this before resuming work on a ticket."

const searchDesc = "Full-text search across ticket titles, descriptions, and comments, with optional status/project/label filters. Use before creating a ticket to check whether one already exists, and to find past decisions ('how did we handle X')."

const contextDesc = "Get a working-state report: everything in progress, everything blocked (with reasons), and the top of the todo queue. Call this at the start of every session before deciding what to do."

// --- input types ---

type CreateTicketInput struct {
	Title       string   `json:"title" jsonschema:"imperative mood, e.g. 'Add Zigbee sensor bridge'"`
	Description string   `json:"description,omitempty" jsonschema:"goal, constraints, acceptance criteria"`
	Project     string   `json:"project,omitempty" jsonschema:"defaults to 'default'"`
	ParentKey   string   `json:"parent_key,omitempty" jsonschema:"e.g. T-7; makes this a subtask"`
	Priority    string   `json:"priority,omitempty" jsonschema:"critical|high|normal|low, default normal"`
	Labels      []string `json:"labels,omitempty"`
}

type UpdateTicketInput struct {
	Key         string   `json:"key" jsonschema:"required, e.g. T-42"`
	Status      string   `json:"status,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty" jsonschema:"replaces description; prefer add_comment for progress notes"`
	Labels      []string `json:"labels,omitempty" jsonschema:"replaces the full label set"`
	LinkBlocks  string   `json:"link_blocks,omitempty" jsonschema:"key of a ticket this one blocks"`
	LinkRelates string   `json:"link_relates,omitempty" jsonschema:"key of a related ticket"`
}

type AddCommentInput struct {
	Key    string `json:"key" jsonschema:"required"`
	Body   string `json:"body" jsonschema:"required. Markdown allowed."`
	Author string `json:"author,omitempty" jsonschema:"default 'agent'"`
}

type GetTicketInput struct {
	Key string `json:"key" jsonschema:"required"`
}

type SearchTicketsInput struct {
	Query   string `json:"query,omitempty" jsonschema:"FTS5 query; omit to filter only"`
	Status  string `json:"status,omitempty"`
	Project string `json:"project,omitempty"`
	Label   string `json:"label,omitempty"`
	Limit   int    `json:"limit,omitempty" jsonschema:"default 10, max 50"`
}

type GetContextInput struct {
	Project string `json:"project,omitempty" jsonschema:"omit for all projects"`
}

// --- handlers ---

func handleCreate(st *store.Store) mcp.ToolHandlerFor[CreateTicketInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateTicketInput) (*mcp.CallToolResult, any, error) {
		prio, err := domain.ParsePriority(in.Priority)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		t, existed, err := st.CreateTicket(ctx, store.CreateParams{
			Title:       in.Title,
			Description: in.Description,
			Project:     in.Project,
			ParentKey:   in.ParentKey,
			Priority:    prio,
			Labels:      in.Labels,
		})
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		return textResult(Ticket(t, existed)), nil, nil
	}
}

func handleUpdate(st *store.Store) mcp.ToolHandlerFor[UpdateTicketInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateTicketInput) (*mcp.CallToolResult, any, error) {
		p := store.UpdateParams{
			Key:         in.Key,
			LinkBlocks:  in.LinkBlocks,
			LinkRelates: in.LinkRelates,
		}
		if in.Status != "" {
			s, err := domain.ParseStatus(in.Status)
			if err != nil {
				return errResult(err.Error()), nil, nil
			}
			p.Status = &s
		}
		if in.Priority != "" {
			pr, err := domain.ParsePriority(in.Priority)
			if err != nil {
				return errResult(err.Error()), nil, nil
			}
			p.Priority = &pr
		}
		if in.Title != "" {
			p.Title = &in.Title
		}
		if in.Description != "" {
			p.Description = &in.Description
		}
		if in.Labels != nil {
			p.Labels = &in.Labels
		}

		t, summary, err := st.UpdateTicket(ctx, p)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		// Header + one-line confirmation of what changed.
		return textResult(ticketHeader(t) + "\n" + summary), nil, nil
	}
}

func handleComment(st *store.Store) mcp.ToolHandlerFor[AddCommentInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddCommentInput) (*mcp.CallToolResult, any, error) {
		count, err := st.AddComment(ctx, in.Key, in.Author, in.Body)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		msg := commentConfirmation(in.Key, count)
		// Soft-warn on large comments per design open-question #2.
		if len(in.Body) > 4096 {
			msg += "\nNote: this comment is large (>4 KB). Consider splitting durable state into a description update plus a shorter note."
		}
		return textResult(msg), nil, nil
	}
}

func handleGet(st *store.Store) mcp.ToolHandlerFor[GetTicketInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetTicketInput) (*mcp.CallToolResult, any, error) {
		t, err := st.GetTicketFull(ctx, in.Key)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		return textResult(Ticket(t, false)), nil, nil
	}
}

func handleSearch(st *store.Store) mcp.ToolHandlerFor[SearchTicketsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchTicketsInput) (*mcp.CallToolResult, any, error) {
		res, err := st.Search(ctx, store.SearchParams{
			Query:   in.Query,
			Status:  in.Status,
			Project: in.Project,
			Label:   in.Label,
			Limit:   in.Limit,
		})
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		return textResult(SearchResults(in.Query, res)), nil, nil
	}
}

func handleContext(st *store.Store) mcp.ToolHandlerFor[GetContextInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetContextInput) (*mcp.CallToolResult, any, error) {
		rep, err := st.ContextReport(ctx, in.Project)
		if err != nil {
			return errResult(err.Error()), nil, nil
		}
		return textResult(Context(rep, now().Local().Format("2006-01-02"))), nil, nil
	}
}
