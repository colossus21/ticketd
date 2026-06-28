// Command ticketd is an agent-native ticket tracker exposed over MCP.
// By default it serves the six MCP tools over stdio for Claude Code.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/colossus21/ticketd/internal/cli"
	"github.com/colossus21/ticketd/internal/mcptools"
	"github.com/colossus21/ticketd/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Build metadata. version is the release; commit and date are injected at
// build time via -ldflags "-X main.commit=... -X main.date=...". When built
// with plain `go build`, commit falls back to the VCS info embedded by the Go
// toolchain (see resolveCommit).
var (
	version = "0.2.0"
	commit  = ""
	date    = ""
)

func main() {
	dbPath := flag.String("db", defaultDBPath(), "path to SQLite database")
	transport := flag.String("transport", "stdio", "stdio | http")
	addr := flag.String("addr", "127.0.0.1:7333", "listen address for http transport")
	token := flag.String("token", os.Getenv("TICKETD_TOKEN"),
		"bearer token required on the HTTP MCP endpoint (default $TICKETD_TOKEN; empty = no auth)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	// --version flag, or `ticketd version` subcommand.
	if *showVersion || (len(flag.Args()) > 0 && flag.Args()[0] == "version") {
		fmt.Println(versionString())
		return
	}

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

	// Cancel the root context on SIGINT/SIGTERM so both transports shut down
	// cleanly (stdio's Run returns; HTTP drains in-flight requests).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		slog.Info("ticketd starting", "transport", "stdio", "db", *dbPath)
		err = srv.Run(ctx, &mcp.StdioTransport{})
	case "http":
		err = serveHTTP(ctx, srv, st, *addr, *token)
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

// versionString renders the build version, preferring ldflags-injected values
// and falling back to the toolchain's embedded VCS info for plain `go build`.
func versionString() string {
	c, d := resolveCommit()
	s := "ticketd " + version
	if c != "" {
		s += " (" + c + ")"
	}
	if d != "" {
		s += " built " + d
	}
	return s
}

// resolveCommit returns the commit SHA and build date, from ldflags if set,
// otherwise from runtime/debug build info.
func resolveCommit() (string, string) {
	c, d := commit, date
	if c != "" {
		return c, d
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 7 {
					c = s.Value[:7]
				} else {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			}
		}
	}
	return c, d
}
