// Package cli provides human-facing subcommands (ls/show/comment) that read
// and write the same store the MCP server uses. It is a thin consumer of store
// and mcptools rendering — no SQL of its own.
package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/rafiulalam/ticketd/internal/domain"
	"github.com/rafiulalam/ticketd/internal/mcptools"
	"github.com/rafiulalam/ticketd/internal/store"
)

// stringSlice collects a repeatable string flag (e.g. --label a --label b).
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// Run dispatches a CLI subcommand. args is everything after the global flags
// (i.e. flag.Args()). Returns a process exit code.
func Run(args []string, dbPath string) int {
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()

	switch args[0] {
	case "create", "new":
		return cmdCreate(ctx, st, args[1:])
	case "ls":
		return cmdLs(ctx, st, args[1:])
	case "show":
		return cmdShow(ctx, st, args[1:])
	case "comment":
		return cmdComment(ctx, st, args[1:])
	case "context":
		return cmdContext(ctx, st, args[1:])
	case "backup":
		return cmdBackup(ctx, st, dbPath, args[1:])
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ticketd — agent-native ticket tracker

Usage:
  ticketd                          run the MCP server over stdio (default)
  ticketd --transport http         run the MCP server over HTTP
  ticketd create "Title" [--priority P] [--project P] [--parent T-7] [--label L ...]
                                   create a ticket from the terminal
  ticketd ls [--status S] [--project P]   list tickets
  ticketd show T-42                show a ticket with its full worklog
  ticketd comment T-42 "text"      append a worklog comment (author = $USER)
  ticketd context [--project P]    print the working-state report
  ticketd backup [--dir D]         write a timestamped VACUUM INTO copy

Global flags (before the subcommand):
  --db PATH    path to the SQLite database
`)
}

func cmdCreate(ctx context.Context, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	desc := fs.String("description", "", "ticket description")
	project := fs.String("project", "", "project (default 'default')")
	parent := fs.String("parent", "", "parent ticket key, e.g. T-7")
	priority := fs.String("priority", "", "critical|high|normal|low")
	var labels stringSlice
	fs.Var(&labels, "label", "label (repeatable)")

	// The title is the leading positional argument; Go's flag package stops at
	// the first non-flag token, so peel the title off before parsing flags.
	// This lets flags follow the title: create "Title" --priority high.
	var title string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		title = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if title == "" {
		title = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if title == "" {
		fmt.Fprintln(os.Stderr, `usage: ticketd create "Title" [--priority P] [--project P] [--parent T-7] [--label L ...]`)
		return 2
	}
	prio, err := domain.ParsePriority(*priority)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	t, existed, err := st.CreateTicket(ctx, store.CreateParams{
		Title:       title,
		Description: *desc,
		Project:     *project,
		ParentKey:   *parent,
		Priority:    prio,
		Labels:      labels,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Print(mcptools.Ticket(t, existed))
	return 0
}

func cmdLs(ctx context.Context, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	status := fs.String("status", "", "filter by status")
	project := fs.String("project", "", "filter by project")
	label := fs.String("label", "", "filter by label")
	limit := fs.Int("limit", 50, "max results")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	res, err := st.Search(ctx, store.SearchParams{
		Status: *status, Project: *project, Label: *label, Limit: *limit,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Print(mcptools.SearchResults("", res))
	return 0
}

func cmdShow(ctx context.Context, st *store.Store, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: ticketd show T-42")
		return 2
	}
	t, err := st.GetTicketFull(ctx, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Print(mcptools.Ticket(t, false))
	return 0
}

func cmdComment(ctx context.Context, st *store.Store, args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: ticketd comment T-42 "your note"`)
		return 2
	}
	author := "human"
	if u, err := user.Current(); err == nil && u.Username != "" {
		author = u.Username
	}
	n, err := st.AddComment(ctx, args[0], author, args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("Comment added to %s (%d total).\n", args[0], n)
	return 0
}

func cmdBackup(ctx context.Context, st *store.Store, dbPath string, args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dir := fs.String("dir", filepath.Dir(dbPath), "directory to write the backup into")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := filepath.Base(dbPath)
	stamp := time.Now().Format("20060102-150405")
	dest := filepath.Join(*dir, base+"."+stamp+".bak")
	if err := st.Backup(ctx, dest); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("Backup written to %s\n", dest)
	return 0
}

func cmdContext(ctx context.Context, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	project := fs.String("project", "", "filter by project")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rep, err := st.ContextReport(ctx, *project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Print(mcptools.RenderContextReport(rep))
	return 0
}
