package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	t.Run("disabled passes through", func(t *testing.T) {
		reached = false
		h := bearerAuth("", next)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if !reached || rec.Code != http.StatusOK {
			t.Fatalf("no-token should pass through; reached=%v code=%d", reached, rec.Code)
		}
	})

	t.Run("missing token rejected", func(t *testing.T) {
		reached = false
		h := bearerAuth("s3cret", next)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if reached || rec.Code != http.StatusUnauthorized {
			t.Fatalf("missing token should be 401; reached=%v code=%d", reached, rec.Code)
		}
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		reached = false
		h := bearerAuth("s3cret", next)
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer nope")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if reached || rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong token should be 401; reached=%v code=%d", reached, rec.Code)
		}
	})

	t.Run("correct token allowed", func(t *testing.T) {
		reached = false
		h := bearerAuth("s3cret", next)
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !reached || rec.Code != http.StatusOK {
			t.Fatalf("correct token should pass; reached=%v code=%d", reached, rec.Code)
		}
	})
}

func TestRootRouter(t *testing.T) {
	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	h := rootRouter(next)

	t.Run("GET / redirects to board", func(t *testing.T) {
		reached = false
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/board" {
			t.Fatalf("GET / should 302 to /board; code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
		}
		if reached {
			t.Fatal("MCP handler should not be reached for browser GET /")
		}
	})

	t.Run("POST / passes through to MCP", func(t *testing.T) {
		reached = false
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if !reached {
			t.Fatal("POST / should reach the MCP handler")
		}
	})
}

func TestRecoverMiddleware(t *testing.T) {
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	rec := httptest.NewRecorder()
	// Should not propagate the panic.
	recoverMiddleware(panicky).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic should become 500, got %d", rec.Code)
	}
}
