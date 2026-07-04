package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAutoDriver_PostsCredsAndSucceeds(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive" || r.Method != "POST" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	d := NewAutoDriver(srv.URL)
	err := d.Drive(context.Background(), "https://accounts.google.com/o", &GoogleCred{Email: "a@x.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["oauth_url"] != "https://accounts.google.com/o" || gotBody["email"] != "a@x.com" || gotBody["password"] != "pw" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestAutoDriver_FailureReasonBecomesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "reason": "wrong password"})
	}))
	defer srv.Close()
	err := NewAutoDriver(srv.URL).Drive(context.Background(), "u", &GoogleCred{Email: "a@x.com", Password: "bad"})
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("expected error containing reason, got %v", err)
	}
}

func TestAutoDriver_NilCredErrors(t *testing.T) {
	if err := NewAutoDriver("http://127.0.0.1:1").Drive(context.Background(), "u", nil); err == nil {
		t.Error("expected error for nil credentials")
	}
}
