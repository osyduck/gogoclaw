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

func TestPickPython(t *testing.T) {
	canImport := func(p string) bool { return p == "good" }

	if got := pickPython([]string{"", "bad", "good"}, canImport); got != "good" {
		t.Errorf("pickPython = %q, want %q", got, "good")
	}
	if got := pickPython([]string{"", "bad", "worse"}, canImport); got != "" {
		t.Errorf("pickPython = %q, want empty string", got)
	}
}
