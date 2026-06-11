// Command ticketd is an agent-native ticket tracker exposed over MCP.
// By default it serves the six MCP tools over stdio for Claude Code.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafiulalam/ticketd/internal/cli"
	"github.com/rafiulalam/ticketd/internal/mcptools"
	"github.com/rafiulalam/ticketd/internal/store"
)

var version = "0.1.0"

func main() {
	dbPath := flag.String("db", defaultDBPath(), "path to SQLite database")
	transport := flag.String("transport", "stdio", "stdio | http")
	addr := flag.String("addr", "127.0.0.1:7333", "listen address for http transport")
	flag.Parse()

	// All logging goes to stderr; stdout is reserved for the MCP protocol.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// CLI mode: any positional args dispatch to a subcommand (ls/show/...).
	if rest := flag.Args(); len(rest) > 0 {
		os.Exit(cli.Run(rest, *dbPath))
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		fatal(err)
	}
	defer st.Close()

	srv := mcp.NewServer(&mcp.Implementation{Name: "tickets", Version: version}, nil)
	mcptools.Register(srv, st)

	ctx := context.Background()
	switch *transport {
	case "stdio":
		slog.Info("ticketd starting", "transport", "stdio", "db", *dbPath)
		err = srv.Run(ctx, &mcp.StdioTransport{})
	case "http":
		err = serveHTTP(ctx, srv, *addr)
	default:
		fatal(fmt.Errorf("unknown transport %q: use stdio or http", *transport))
	}
	if err != nil {
		fatal(err)
	}
}

// defaultDBPath resolves ~/.local/share/ticketd/tickets.db, creating the dir.
func defaultDBPath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "tickets.db"
		}
		dir = filepath.Join(home, ".local", "share")
	}
	dir = filepath.Join(dir, "ticketd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "tickets.db"
	}
	return filepath.Join(dir, "tickets.db")
}

func fatal(err error) {
	slog.Error("fatal", "err", err)
	os.Exit(1)
}
