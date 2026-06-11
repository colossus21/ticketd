package main

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafiulalam/ticketd/internal/store"
	"github.com/rafiulalam/ticketd/internal/web"
)

// serveHTTP serves the MCP server (streamable HTTP) plus a read-only HTML
// board. Bind to 127.0.0.1 and tunnel over SSH/Tailscale for remote access;
// set a token (flag or $TICKETD_TOKEN) to require bearer auth on the
// write-capable MCP endpoint when the box is exposed.
func serveHTTP(ctx context.Context, srv *mcp.Server, st *store.Store, addr, token string) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/board", web.BoardHandler(st))
	mux.Handle("/", bearerAuth(token, mcpHandler))

	httpSrv := &http.Server{Addr: addr, Handler: mux}
	authNote := "disabled"
	if token != "" {
		authNote = "bearer-token required"
	}
	slog.Info("ticketd starting", "transport", "http", "addr", addr,
		"board", "http://"+addr+"/board", "mcp_auth", authNote)

	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	return httpSrv.ListenAndServe()
}

// bearerAuth wraps next, requiring an "Authorization: Bearer <token>" header
// when token is non-empty. The comparison is constant-time. When token is
// empty, auth is disabled and the wrapper is a pass-through.
func bearerAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ticketd"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
