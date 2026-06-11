package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serveHTTP serves the MCP server over the SDK's streamable HTTP transport.
// Bind to 127.0.0.1 and tunnel over SSH/Tailscale for remote access; a static
// bearer-token middleware can be layered here for an exposed box (phase 4).
func serveHTTP(ctx context.Context, srv *mcp.Server, addr string) error {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	httpSrv := &http.Server{Addr: addr, Handler: handler}
	slog.Info("ticketd starting", "transport", "http", "addr", addr)

	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	return httpSrv.ListenAndServe()
}
