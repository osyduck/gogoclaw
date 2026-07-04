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
	h, refresher := buildHandler(st, api.NewClient(), events.New())
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
