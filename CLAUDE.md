# ticketd

Agent-native ticket tracker exposed over MCP. Single Go binary + single SQLite
file. See `docs/DESIGN.md` for the full rationale.

## Build & test

```sh
go build -o ticketd ./cmd/ticketd
go test ./...            # add -race before committing concurrency changes
go vet ./...
```

When the agent-facing Markdown output changes, regenerate golden files
deliberately and review the diff: `go test ./internal/mcptools -update`.

## Architecture (import direction is the only layering rule)

- `internal/domain` — types + status FSM. Pure; imports nothing internal.
  Transition errors must name the legal next states so the agent self-corrects.
- `internal/store` — all SQL; the *only* package importing the sqlite driver.
  Exposes domain types, never `*sql.Row`. Write transactions use
  `BEGIN IMMEDIATE` (`_txlock=immediate`) to avoid read→write deadlocks.
- `internal/mcptools` — thin glue: input structs → store calls → Markdown.
  Validation errors return as tool-result text (`IsError`), not Go errors.
- `internal/web` — read-only HTML board. `internal/cli` — human subcommands.
- `cmd/ticketd` — flag parsing, transport/CLI dispatch, HTTP + bearer auth.

stdout belongs to the MCP protocol on stdio; all logging goes to stderr.

## Ticket tracking — use the `tickets` MCP server while working here

- At session start, call `get_context` (project `ticketd`) before deciding what
  to do.
- Before any multi-step task, `search_tickets` for an existing ticket, else
  `create_ticket` with `project: "ticketd"`. Move it to `in_progress` when you
  start.
- `add_comment` when you: form a plan, make a non-obvious decision, reject an
  approach, hit a blocker, or finish. Write for a future session with zero
  context.
- Reference ticket keys (T-42) in commit messages.
- Never mark a ticket done without a closing comment summarizing the outcome
  and listing the files changed.
