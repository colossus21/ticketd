# ticketd — An Agent-Native Ticket Tracker with MCP

**Version:** 0.1 (design draft)
**Author:** Rafiul Alam
**Status:** Pre-implementation

---

## 1. Overview

`ticketd` is a lightweight, Jira-like ticket tracker designed **exclusively for AI agents** (primarily Claude Code) via the Model Context Protocol. It is not a human project-management tool with an API bolted on; it is durable external memory for agents, with a thin human inspection layer on the side.

### 1.1 Problem statement

AI coding agents lose context between sessions. Work that spans multiple sessions — multi-step features, investigations, refactors — requires the human to re-explain state every time. Existing trackers (Jira, Linear, GitHub Issues) are built around human workflows, human UIs, and heavyweight APIs that agents use poorly and reluctantly.

### 1.2 Core insight

Design for the agent's failure modes, not the human's. Humans forget to update tickets; agents forget **context**. Therefore:

- A ticket must contain enough state for a **cold-start agent session to resume work**: decisions made, files touched, approaches tried and rejected, open questions.
- The worklog (comments) is the primary artifact, not the status field.
- Tool outputs are rendered as **Markdown for model consumption**, not JSON for machine consumption.
- Error messages are part of the API surface — agents read them and self-correct.

### 1.3 Goals

- Single static Go binary, single SQLite file. Zero infrastructure.
- Six well-designed MCP tools over stdio (local) and streamable HTTP (remote, later).
- Tickets as serialized agent context: structured worklog, decomposition into subtasks, blocking links.
- Multi-project support in one database.
- A `get_context` tool that gives an agent full situational awareness in one call.

### 1.4 Non-goals (v0.x)

- Web UI with write access (read-only board is a stretch goal).
- Multi-user auth, permissions, audit trails.
- Sprints, estimates, burndown, custom fields, custom workflows.
- Configurable status machines.

---

## 2. Architecture

```
┌────────────────┐  stdio (MCP)   ┌──────────────────────────────┐
│  Claude Code   │ ◄────────────► │  ticketd                     │
└────────────────┘                │  ┌────────────────────────┐  │
                                  │  │ internal/mcptools      │  │
┌────────────────┐  HTTP (MCP)    │  │  (thin glue, MD render)│  │
│  Remote agent  │ ◄────────────► │  ├────────────────────────┤  │
└────────────────┘   (phase 4)    │  │ internal/domain        │  │
                                  │  │  (types, status FSM)   │  │
┌────────────────┐  direct call   │  ├────────────────────────┤  │
│  CLI subcmds   │ ──────────────►│  │ internal/store         │  │
│  (ticketd ls)  │                │  │  (SQLite, FTS5)        │  │
└────────────────┘                │  └───────────┬────────────┘  │
                                  └──────────────┼───────────────┘
                                                 ▼
                                          tickets.db (SQLite, WAL)
```

**Layering rule:** `store` and `domain` know nothing about MCP. The MCP layer, the CLI, and any future web UI are all thin consumers of the same store. This is enforced by import direction only — no interfaces for their own sake.

### 2.1 Repository layout

```
ticketd/
├── cmd/
│   └── ticketd/
│       └── main.go            # flag parsing, wiring, transport/CLI dispatch
├── internal/
│   ├── domain/
│   │   ├── ticket.go          # Ticket, Comment, Link, Priority types
│   │   ├── transitions.go     # status FSM + validation errors
│   │   └── transitions_test.go
│   ├── store/
│   │   ├── store.go           # all SQL; the only package importing sqlite
│   │   ├── migrations.go      # PRAGMA user_version-gated migrations
│   │   ├── search.go          # FTS5 queries
│   │   └── store_test.go      # against real temp-file SQLite
│   ├── mcptools/
│   │   ├── tools.go           # tool registration + handlers
│   │   ├── render.go          # domain types -> Markdown
│   │   └── tools_test.go      # via InMemoryTransport
│   └── cli/
│       └── cli.go             # ls/show/comment subcommands (phase 4)
├── .mcp.json.example
├── CLAUDE.md.example          # snippet users paste into their projects
├── go.mod
└── README.md
```

### 2.2 Dependencies

| Dependency | Purpose | Notes |
|---|---|---|
| `modernc.org/sqlite` | Database | Pure Go, no CGO → trivial cross-compile (mini PC, Pi, Hetzner). FTS5 included. |
| `github.com/modelcontextprotocol/go-sdk/mcp` | MCP server | Official SDK. Typed tool handlers, schema derived from input structs, stdio + streamable HTTP transports, `InMemoryTransport` for tests. |

Nothing else. No web framework, no ORM, no migration library, no logging framework (stdlib `log/slog`).

---

## 3. Data model

### 3.1 Schema (migration 001)

```sql
PRAGMA journal_mode = WAL;

CREATE TABLE tickets (
    id          INTEGER PRIMARY KEY,
    key         TEXT UNIQUE NOT NULL,            -- "T-42"
    project     TEXT NOT NULL DEFAULT 'default',
    title       TEXT NOT NULL CHECK (length(title) > 0),
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'todo',
    priority    INTEGER NOT NULL DEFAULT 2,      -- 0=critical 1=high 2=normal 3=low
    parent_id   INTEGER REFERENCES tickets(id),
    labels      TEXT NOT NULL DEFAULT '[]',      -- JSON array of strings
    created_at  TEXT NOT NULL,                   -- RFC3339 UTC
    updated_at  TEXT NOT NULL
);

CREATE INDEX idx_tickets_project_status ON tickets(project, status);
CREATE INDEX idx_tickets_parent ON tickets(parent_id);

CREATE TABLE comments (
    id         INTEGER PRIMARY KEY,
    ticket_id  INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    author     TEXT NOT NULL,                    -- 'agent' | human name
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_comments_ticket ON comments(ticket_id, created_at);

CREATE TABLE links (
    from_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    to_id   INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    kind    TEXT NOT NULL,                       -- 'blocks' | 'relates'
    PRIMARY KEY (from_id, to_id, kind)
);

CREATE TABLE counters (
    project TEXT PRIMARY KEY,
    next    INTEGER NOT NULL
);

-- Full-text search over tickets and comments
CREATE VIRTUAL TABLE tickets_fts USING fts5(
    title, description, content='tickets', content_rowid='id'
);
CREATE VIRTUAL TABLE comments_fts USING fts5(
    body, content='comments', content_rowid='id'
);

-- Sync triggers (insert/update/delete) for both FTS tables
CREATE TRIGGER tickets_ai AFTER INSERT ON tickets BEGIN
    INSERT INTO tickets_fts(rowid, title, description)
    VALUES (new.id, new.title, new.description);
END;
CREATE TRIGGER tickets_au AFTER UPDATE ON tickets BEGIN
    INSERT INTO tickets_fts(tickets_fts, rowid, title, description)
    VALUES ('delete', old.id, old.title, old.description);
    INSERT INTO tickets_fts(rowid, title, description)
    VALUES (new.id, new.title, new.description);
END;
CREATE TRIGGER tickets_ad AFTER DELETE ON tickets BEGIN
    INSERT INTO tickets_fts(tickets_fts, rowid, title, description)
    VALUES ('delete', old.id, old.title, old.description);
END;
CREATE TRIGGER comments_ai AFTER INSERT ON comments BEGIN
    INSERT INTO comments_fts(rowid, body) VALUES (new.id, new.body);
END;
CREATE TRIGGER comments_ad AFTER DELETE ON comments BEGIN
    INSERT INTO comments_fts(comments_fts, rowid, body)
    VALUES ('delete', old.id, old.body);
END;
```

Migrations are gated by `PRAGMA user_version`: read it at startup, apply numbered migrations above it inside a transaction, bump it. ~40 lines of code, no library.

**Connection pragmas (every open):** `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`.

### 3.2 Key generation

Keys are short and human-typeable (`T-42`) because agents reference them in conversation, commit messages, and comments. Generated atomically from the `counters` table:

```sql
INSERT INTO counters(project, next) VALUES (?, 2)
ON CONFLICT(project) DO UPDATE SET next = next + 1
RETURNING next - 1;
```

Single project shares one `T-` prefix in v0; if cross-project key collisions ever matter, switch to per-project prefixes (`VA-12`, `PX-3`) in a later migration — the `key` column is opaque to all other code.

### 3.3 Status machine

```
backlog ──► todo ──► in_progress ──► in_review ──► done
              ▲        │   ▲              │
              │        ▼   │              ▼
              └───── blocked ◄────── (back to in_progress)

wont_do reachable from: backlog, todo, blocked
```

Defined in `internal/domain/transitions.go`:

```go
type Status string

const (
    Backlog    Status = "backlog"
    Todo       Status = "todo"
    InProgress Status = "in_progress"
    InReview   Status = "in_review"
    Blocked    Status = "blocked"
    Done       Status = "done"
    WontDo     Status = "wont_do"
)

var transitions = map[Status][]Status{
    Backlog:    {Todo, WontDo},
    Todo:       {InProgress, Blocked, WontDo},
    InProgress: {InReview, Done, Blocked, Todo},
    InReview:   {Done, InProgress},
    Blocked:    {Todo, InProgress, WontDo},
    Done:       {},      // terminal
    WontDo:     {},      // terminal
}

// ErrIllegalTransition messages NAME the legal next states, because
// the agent reads the error string and self-corrects:
//   "cannot move T-42 from done to in_progress: done is terminal.
//    Create a new ticket instead."
//   "cannot move T-7 from backlog to in_review: legal transitions
//    from backlog are: todo, wont_do"
```

**Design rule:** every validation error must tell the agent what to do instead.

### 3.4 Priority

Integer 0–3 (`critical`, `high`, `normal`, `low`). Tools accept and render the string names; the store keeps the integer for sorting.

---

## 4. MCP tool surface

Six tools. Fewer, smarter tools beat 1:1 CRUD. Every tool description is treated as prompt engineering: it encodes the workflow conventions the agent should follow.

All outputs are **Markdown**, never raw JSON — token-efficient and natively legible to the model.

### 4.1 `create_ticket`

> **Description (verbatim in code):** "Create a ticket. Use when starting any non-trivial or multi-step piece of work. Returns the ticket key (e.g. T-42); reference it in commit messages and comments. For large work, create a parent ticket then subtasks via parent_key."

```go
type CreateTicketInput struct {
    Title       string   `json:"title" jsonschema:"required. Imperative mood, e.g. 'Add Zigbee sensor bridge'"`
    Description string   `json:"description,omitempty" jsonschema:"goal, constraints, acceptance criteria"`
    Project     string   `json:"project,omitempty" jsonschema:"defaults to 'default'"`
    ParentKey   string   `json:"parent_key,omitempty" jsonschema:"e.g. T-7; makes this a subtask"`
    Priority    string   `json:"priority,omitempty" jsonschema:"critical|high|normal|low, default normal"`
    Labels      []string `json:"labels,omitempty"`
}
```

**Behavior:**
- New tickets start in `todo` (use `update_ticket` to push to `backlog` if parked).
- **Idempotency:** if an open (non-terminal) ticket in the same project has a byte-identical title, return that ticket with a leading note: `Note: an open ticket with this exact title already exists; returning it instead of creating a duplicate.` Agents retry; duplicates are worse than the rare false positive.
- Returns the full rendered ticket (see §4.4 format).

### 4.2 `update_ticket`

> **Description:** "Update a ticket's status, priority, title, description, or labels. Status transitions are validated; on rejection the error lists the legal moves. When moving to blocked, immediately add_comment explaining the blocker. When moving to done, immediately add_comment summarizing the outcome."

```go
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
```

**Behavior:** partial update; only provided fields change. Status validated through the FSM. Returns the rendered ticket header + a one-line confirmation of what changed.

### 4.3 `add_comment`

> **Description:** "Append a worklog entry to a ticket. Comment whenever you: form a plan, make a non-obvious decision, touch files worth recording, try an approach that fails, hit a blocker, or finish. Comments are append-only and are the memory a future session resumes from — write for a reader with zero context."

```go
type AddCommentInput struct {
    Key    string `json:"key" jsonschema:"required"`
    Body   string `json:"body" jsonschema:"required. Markdown allowed."`
    Author string `json:"author,omitempty" jsonschema:"default 'agent'"`
}
```

**Behavior:** append-only, no edit/delete tools exposed. Touches `tickets.updated_at`. Returns `Comment added to T-42 (5 comments total).`

### 4.4 `get_ticket`

> **Description:** "Fetch a ticket with its full worklog, subtasks, and links in one call. Call this before resuming work on a ticket."

```go
type GetTicketInput struct {
    Key string `json:"key" jsonschema:"required"`
}
```

**Output format (canonical render, used by several tools):**

```markdown
# T-42: Add Zigbee sensor MCP bridge
status: in_progress · priority: high · project: voice-assistant
parent: T-40 · labels: mqtt, home-assistant
created: 2026-06-10 · updated: 2026-06-11

Bridge zigbee2mqtt sensor topics into the assistant's MCP server so
Claude can answer "what's the temperature in the bedroom".

## Subtasks
- [x] T-43: Define sensor reading schema (done)
- [ ] T-44: Subscribe to zigbee2mqtt/+ (in_progress)

## Links
- blocks T-39: Voice query routing
- relates T-21: Home Assistant token setup

## Worklog (3 comments)
**[2026-06-10 18:30] agent** — Plan: paho-mqtt client, subscribe to
zigbee2mqtt/+, cache last reading per device, expose get_sensor tool.
**[2026-06-11 09:14] agent** — Decision: cache in-memory only; HA
already persists history. Files: internal/sensors/cache.go.
**[2026-06-11 10:02] agent** — BLOCKED: HA long-lived token lacks
required scope. Asked Rafiul. See T-21.
```

One call returns everything; never force the agent into N round trips.

### 4.5 `search_tickets`

> **Description:** "Full-text search across ticket titles, descriptions, and comments, with optional status/project/label filters. Use before creating a ticket to check whether one already exists, and to find past decisions ('how did we handle X')."

```go
type SearchTicketsInput struct {
    Query   string `json:"query,omitempty" jsonschema:"FTS5 query; omit to filter only"`
    Status  string `json:"status,omitempty"`
    Project string `json:"project,omitempty"`
    Label   string `json:"label,omitempty"`
    Limit   int    `json:"limit,omitempty" jsonschema:"default 10, max 50"`
}
```

**Behavior:** queries `tickets_fts` and `comments_fts`, merges by ticket, ranks by bm25. Sanitize the query (wrap terms in `"`) so user text can't break FTS5 syntax. Output is a compact list:

```markdown
3 results for "zigbee":
- T-42 [in_progress, high] Add Zigbee sensor MCP bridge — matched title, 2 comments
- T-21 [done] Home Assistant token setup — matched comment: "...zigbee2mqtt auth..."
- T-19 [wont_do] Zigbee direct serial — matched title
```

### 4.6 `get_context`

> **Description:** "Get a working-state report: everything in progress, everything blocked (with reasons), and the top of the todo queue. **Call this at the start of every session** before deciding what to do."

```go
type GetContextInput struct {
    Project string `json:"project,omitempty" jsonschema:"omit for all projects"`
}
```

**Output — a report, not a query dump:**

```markdown
# Context: voice-assistant — 2026-06-11

## In progress (1)
### T-42: Add Zigbee sensor MCP bridge [high]
Last activity 2026-06-11 10:02:
> BLOCKED: HA long-lived token lacks required scope. Asked Rafiul. See T-21.

## Blocked (1)
- T-42 → blocked on T-21 (Home Assistant token setup)

## Next up (top 5 todo by priority)
1. T-44 [high] Subscribe to zigbee2mqtt/+  (subtask of T-42)
2. T-45 [normal] Expose get_sensor MCP tool
3. T-38 [normal] Piper voice model upgrade
4. T-50 [low] README for ticketd
5. T-31 [low] ES-DE theme tweaks

(12 tickets in backlog · 27 done · use search_tickets to dig deeper)
```

This is the tool the whole system lives or dies on. The "last activity" excerpt per in-progress ticket is what lets a cold session resume intelligently.

---

## 5. MCP server implementation

### 5.1 Wiring (`cmd/ticketd/main.go`)

```go
func main() {
    dbPath := flag.String("db", defaultDBPath(), "path to SQLite database")
    transport := flag.String("transport", "stdio", "stdio | http")
    addr := flag.String("addr", "127.0.0.1:7333", "listen address for http")
    flag.Parse()

    // CLI mode: `ticketd ls`, `ticketd show T-42` (phase 4)
    if args := flag.Args(); len(args) > 0 {
        os.Exit(cli.Run(args, *dbPath))
    }

    st, err := store.Open(*dbPath) // runs migrations
    if err != nil { fatal(err) }
    defer st.Close()

    srv := mcp.NewServer(&mcp.Implementation{
        Name: "tickets", Version: version,
    }, nil)
    mcptools.Register(srv, st)

    switch *transport {
    case "stdio":
        err = srv.Run(context.Background(), &mcp.StdioTransport{})
    case "http":
        err = serveHTTP(srv, *addr) // phase 4
    }
    if err != nil { fatal(err) }
}
```

**stdio discipline:** stdout belongs to the MCP protocol. All logging goes to stderr via `slog`. One stray `fmt.Println` corrupts the stream.

### 5.2 Handler shape

```go
func Register(s *mcp.Server, st *store.Store) {
    mcp.AddTool(s, &mcp.Tool{Name: "create_ticket", Description: createDesc},
        handleCreate(st))
    // ... 5 more
}

func handleCreate(st *store.Store) func(
    ctx context.Context, req *mcp.CallToolRequest, in CreateTicketInput,
) (*mcp.CallToolResult, any, error) {
    return func(ctx context.Context, req *mcp.CallToolRequest, in CreateTicketInput) (*mcp.CallToolResult, any, error) {
        t, existed, err := st.CreateTicket(ctx, toDomain(in))
        if err != nil {
            // Domain/validation errors go back as tool results the model
            // can read — not protocol errors.
            return textResult(err.Error()), nil, nil
        }
        return textResult(render.Ticket(t, existed)), nil, nil
    }
}
```

**Error rule:** validation failures return as readable tool result text (so the model sees and adapts); only infrastructure failures (db corruption, ctx cancel) return Go errors.

### 5.3 Rendering (`internal/mcptools/render.go`)

One package owns all Markdown rendering (formats in §4.4–4.6). Timestamps render as `2026-06-11 10:02` local time; stored as RFC3339 UTC.

---

## 6. Claude Code integration

### 6.1 `.mcp.json.example`

```json
{
  "mcpServers": {
    "tickets": {
      "command": "ticketd",
      "args": ["--db", "${HOME}/.local/share/ticketd/tickets.db"]
    }
  }
}
```

Project-scoped (committed to a repo) or user-scoped (`claude mcp add --scope user`) for a global tracker across all projects.

### 6.2 `CLAUDE.md.example`

Tool descriptions make the agent use tools *correctly*; CLAUDE.md makes it use them *unprompted*. Ship this snippet:

```markdown
## Ticket tracking
This project uses the `tickets` MCP server.
- At session start, call `get_context` before deciding what to do.
- Before any multi-step task, `search_tickets` for an existing ticket,
  else `create_ticket`. Move it to in_progress when you start.
- `add_comment` when you: form a plan, make a non-obvious decision,
  reject an approach, hit a blocker, or finish. Write for a future
  session with zero context.
- Reference ticket keys (T-42) in commit messages.
- Never mark a ticket done without a closing comment summarizing the
  outcome and listing the files changed.
```

---

## 7. Build phases

### Phase 1 — Domain + store (~half a day)
- [ ] `domain`: types, status FSM, validation with instructive errors
- [ ] `store`: Open/migrate, CreateTicket (with counter + idempotency), UpdateTicket, AddComment, GetTicketFull, Search, ContextReport
- [ ] Table-driven FSM tests; store tests against temp-file SQLite

**Exit criteria:** `go test ./...` green; can create/comment/transition via a scratch main.

### Phase 2 — MCP layer (~half a day)
- [ ] Six tools registered with final descriptions
- [ ] Markdown renderer with golden-file tests
- [ ] `InMemoryTransport` round-trip tests (create → comment → get_context)

**Exit criteria:** `ticketd` runs under MCP Inspector (`npx @modelcontextprotocol/inspector ticketd`); all tools callable.

### Phase 3 — Claude Code dogfood (~1 hour + iteration)
- [ ] Install binary, add user-scoped MCP config, paste CLAUDE.md snippet
- [ ] First ticket: `T-1: build remaining ticketd features`
- [ ] Iterate on tool descriptions and `get_context` format based on real agent behavior — this is the highest-leverage tuning in the project

### Phase 4 — Conveniences (iterative)
- [ ] CLI subcommands: `ticketd ls [--status --project]`, `ticketd show T-42`, `ticketd comment T-42 "..."` (author = $USER)
- [ ] `--transport http` via the SDK's streamable HTTP transport; bind 127.0.0.1 and tunnel over SSH/Tailscale, or add a static bearer-token middleware for the Hetzner box
- [ ] Read-only HTML board at `/board` (single template, no JS)
- [ ] `ticketd backup` → `VACUUM INTO` a timestamped copy

---

## 8. Testing strategy

| Layer | Approach |
|---|---|
| `domain` FSM | Table-driven: every (from, to) pair, assert allowed/denied and that denial messages list legal moves |
| `store` | Real SQLite in `t.TempDir()`; no mocks. Cover idempotent create, FTS sync after update, context report shape, concurrent comment writes (WAL) |
| `mcptools` render | Golden files for ticket/search/context renders — when output format changes, the diff is reviewed deliberately, because the format is agent-facing API |
| MCP end-to-end | SDK `InMemoryTransport`: client+server in-process, assert tool result text |
| Manual | MCP Inspector before each release; dogfood in Claude Code continuously |

---

## 9. Design decisions log

| # | Decision | Rationale |
|---|---|---|
| 1 | SQLite over Postgres | Single user/agent, single file, zero ops; WAL handles MCP + CLI concurrently |
| 2 | modernc over mattn sqlite | No CGO → painless cross-compilation |
| 3 | Markdown tool outputs | Consumed by a model; cheaper and clearer than JSON |
| 4 | Append-only comments | Worklog is memory; history must survive. No edit/delete tools |
| 5 | Fixed status machine | Configurability adds surface area with no agent benefit in v0 |
| 6 | Validation errors as tool-result text | The agent reads them and self-corrects; protocol errors would be opaque |
| 7 | Idempotent create on exact-title match | Agents retry; duplicates poison get_context |
| 8 | Labels as JSON column, not join table | <10k tickets; FTS covers search; simplicity wins |
| 9 | `get_context` as opinionated report | One call = full situational awareness; the product's core loop |

---

## 10. Open questions

1. **Per-project key prefixes** (`VA-12` vs global `T-12`) — defer until a real collision annoyance appears; opaque `key` column keeps the door open.
2. **Comment size cap?** Probably soft-warn above ~4 KB in the tool result ("consider splitting into description update + shorter note") rather than reject.
3. **Stale in_progress detection** — should `get_context` flag tickets untouched for >7 days? Likely yes in phase 4.
4. **MCP resources** (`ticket://T-42`) in addition to tools — nice for clients that surface resources, but tools cover Claude Code; revisit later.
