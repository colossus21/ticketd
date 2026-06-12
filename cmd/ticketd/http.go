package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/colossus21/ticketd/internal/store"
	"github.com/colossus21/ticketd/internal/web"
)

// HTTP server timeouts. WriteTimeout is intentionally left unset: the MCP
// streamable transport holds long-lived Server-Sent Events connections, and a
// global write deadline would sever them. ReadHeaderTimeout still guards
// against slow-header (Slowloris) clients.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	shutdownGrace     = 10 * time.Second
)

// serveHTTP serves the MCP server (streamable HTTP) plus a read-only HTML
// board. Bind to 127.0.0.1 and tunnel over SSH/Tailscale for remote access;
// set a token (flag or $TICKETD_TOKEN) to require bearer auth on the
// write-capable MCP endpoint when the box is exposed. The server shuts down
// gracefully when ctx is cancelled (SIGINT/SIGTERM).
func serveHTTP(ctx context.Context, srv *mcp.Server, st *store.Store, addr, token string) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/board", web.BoardHandler(st))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	// Root: send browsers to the board; everything else is the MCP endpoint.
	mux.Handle("/", rootRouter(bearerAuth(token, mcpHandler)))

	handler := recoverMiddleware(logMiddleware(mux))

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	authNote := "disabled"
	if token != "" {
		authNote = "bearer-token required"
	}
	slog.Info("ticketd starting", "transport", "http", "addr", addr,
		"board", "http://"+addr+"/board", "mcp_auth", authNote)

	// Shut down gracefully when the context is cancelled.
	errc := make(chan error, 1)
	go func() {
		err := httpSrv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		slog.Info("ticketd shutting down", "grace", shutdownGrace.String())
		sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return httpSrv.Shutdown(sctx)
	}
}

// rootRouter redirects a browser GET on "/" to the board and passes every
// other request (the MCP POST/SSE traffic) through to next.
func rootRouter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			http.Redirect(w, r, "/board", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
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

// statusRecorder captures the response status for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush exposes the underlying flusher so SSE streaming still works through
// the recorder.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logMiddleware emits one structured access log line per request.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "dur", time.Since(start).String(),
			"remote", r.RemoteAddr)
	})
}

// recoverMiddleware turns a handler panic into a 500 and a logged error rather
// than crashing the server.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic in handler", "err", v, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
