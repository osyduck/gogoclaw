package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsure_NoSpawnWhenAlreadyHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	// python is a bogus command; if Ensure tried to spawn it would fail — but the
	// server is already healthy, so Ensure must short-circuit without spawning.
	addr := strings.TrimPrefix(srv.URL, "http://")
	m := New("this-command-does-not-exist", ".", addr)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure should short-circuit on a healthy sidecar, got %v", err)
	}
	if m.cmd != nil {
		t.Error("Ensure spawned a process despite a healthy sidecar")
	}
}

func TestEnsure_SpawnFailure(t *testing.T) {
	// Nothing is listening on this addr and the python command is bogus, so spawn
	// fails fast and Ensure returns an error (rather than hanging).
	m := New("this-command-does-not-exist", ".", "127.0.0.1:59999")
	if err := m.Ensure(context.Background()); err == nil {
		t.Error("expected Ensure to error when the sidecar can't be spawned")
	}
}

func TestURL(t *testing.T) {
	if got := New("python", ".", "127.0.0.1:31500").URL(); got != "http://127.0.0.1:31500" {
		t.Errorf("URL = %s", got)
	}
}
