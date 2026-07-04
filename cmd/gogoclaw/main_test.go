package main

import (
	"net/http/httptest"
	"testing"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

func TestBuildHandler_ServesUIAndAPI(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, refresher := buildHandler(st, api.NewClient(), events.New(), nil)
	if refresher == nil {
		t.Fatal("refresher not built")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Errorf("UI route code = %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/api/accounts", nil))
	if rec2.Code != 200 || rec2.Body.String() == "" {
		t.Errorf("accounts route code = %d body = %s", rec2.Code, rec2.Body)
	}
}

func TestChoosePython(t *testing.T) {
	// "full" has the whole stealth stack; "runner" can run the sidecar (aiohttp)
	// but lacks cloakbrowser — the exact real-world mismatch (a stray venv on
	// PATH) that made bulk logins fail with "No module named 'cloakbrowser'".
	canImport := func(py, mod string) bool {
		switch py {
		case "full":
			return mod == "cloakbrowser" || mod == "aiohttp"
		case "runner":
			return mod == "aiohttp"
		default:
			return false
		}
	}

	// Prefers the cloakbrowser-capable interpreter even when a runner comes first.
	if py, full := choosePython([]string{"", "runner", "full"}, canImport); py != "full" || !full {
		t.Errorf("choosePython = (%q,%v), want (full,true)", py, full)
	}
	// Falls back to aiohttp-only so the sidecar still starts (full=false).
	if py, full := choosePython([]string{"runner", "nope"}, canImport); py != "runner" || full {
		t.Errorf("choosePython = (%q,%v), want (runner,false)", py, full)
	}
	// Nothing usable.
	if py, full := choosePython([]string{"", "nope"}, canImport); py != "" || full {
		t.Errorf("choosePython = (%q,%v), want empty", py, full)
	}
	// A repeated candidate is only considered once and doesn't disturb the result.
	if py, _ := choosePython([]string{"runner", "runner", "full"}, canImport); py != "full" {
		t.Errorf("choosePython dedupe = %q, want full", py)
	}
}
