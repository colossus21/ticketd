# ticketd

An agent-native ticket tracker exposed over the **Model Context Protocol (MCP)**.

`ticketd` is durable external memory for AI coding agents (primarily Claude
Code). A ticket holds enough state for a cold-start session to resume work:
decisions made, files touched, approaches tried and rejected, open questions.
The worklog is the primary artifact, not the status field.

- **Single static Go binary, single SQLite file.** Zero infrastructure, no CGO.
- **Six MCP tools** over stdio (and HTTP). All outputs are Markdown for model
  consumption, not JSON.
- **A `get_context` tool** that gives an agent full situational awareness in one
  call — the core loop of the product.

## Build

```sh
go build -o ticketd ./cmd/ticketd
go test ./...
```

The binary is pure Go (`modernc.org/sqlite`), so it cross-compiles trivially:

```sh
GOOS=linux GOARCH=arm64 go build -o ticketd-linux-arm64 ./cmd/ticketd
```

## Use with Claude Code

Add the server (user scope makes it a global tracker across all projects):

```sh
claude mcp add --scope user tickets -- ticketd --db "$HOME/.local/share/ticketd/tickets.db"
```

or copy [`.mcp.json.example`](.mcp.json.example) to `.mcp.json` in a project.
Then paste [`CLAUDE.md.example`](CLAUDE.md.example) into your project's
`CLAUDE.md` so the agent uses the tools unprompted.

## Tools

| Tool | Purpose |
|---|---|
| `get_context` | Working-state report: in progress, blocked (with reasons), top of the todo queue. Call at session start. |
| `create_ticket` | Start tracking a non-trivial task; idempotent on exact open-title match. |
| `update_ticket` | Status/priority/title/labels + links; status transitions are validated. |
| `add_comment` | Append a worklog entry — the memory a future session resumes from. |
| `get_ticket` | Full ticket with worklog, subtasks, and links in one call. |
| `search_tickets` | Full-text search over titles, descriptions, and comments. |

## CLI (human inspection layer)

```sh
ticketd ls --status in_progress      # list tickets
ticketd show T-42                     # full ticket + worklog
ticketd comment T-42 "looks good"     # append a comment (author = $USER)
ticketd context --project voice       # the working-state report
```

## Transports

```sh
ticketd                          # stdio (default; for Claude Code)
ticketd --transport http --addr 127.0.0.1:7333   # streamable HTTP
```

Bind HTTP to localhost and tunnel over SSH/Tailscale for remote agents.

## Status workflow

```
backlog ──► todo ──► in_progress ──► in_review ──► done
              ▲        │   ▲              │
              │        ▼   │              ▼
              └───── blocked ◄────── (back to in_progress)

wont_do reachable from: backlog, todo, blocked
```

Transitions are validated; rejection messages name the legal next states so the
agent can self-correct. `done` and `wont_do` are terminal.

See [`docs/DESIGN.md`](docs/DESIGN.md) for the full design rationale.
