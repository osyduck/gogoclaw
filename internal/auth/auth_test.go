package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

// tok builds a minimal unsigned JWT ("Bearer h.<payload>.s") whose payload decodes
// to the given claims — so api.ParseClaims reads back exactly this jti/exp.
func tok(jti string, exp int64) string {
	payload, _ := json.Marshal(map[string]any{"jti": jti, "exp": exp, "device_id": "dev"})
	return "Bearer h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

var jwtA = tok("evmsnipe@gmail.com", 1783192918)

func newEngine(t *testing.T, autoglm http.HandlerFunc) (*AuthEngine, store.Store) {
	srv := httptest.NewServer(autoglm)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(api.NewClientWithBase(srv.URL), st, events.New()), st
}

func TestStartLogin_ReturnsStateAndURL(t *testing.T) {
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "https://accounts.google.com/o", "state": "st-1"}})
	})
	state, url, err := e.StartLogin(context.Background(), ManualDriver{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != "st-1" || url != "https://accounts.google.com/o" {
		t.Errorf("state=%q url=%q", state, url)
	}
	s, ok := e.Status("st-1")
	if !ok || s.Status != "pending" {
		t.Errorf("session = %+v ok=%v", s, ok)
	}
}

func TestHandleCallback_PersistsAccount(t *testing.T) {
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/userapi/overseasv1/google-oauth-url":
			data = map[string]any{"oauth_url": "u", "state": "st-2"}
		case "/userapi/overseasv1/google-oauth-login":
			data = map[string]any{"access_token": jwtA, "refresh_token": jwtA, "user_id": "hexid", "user_name": "EVM"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.HandleCallback(context.Background(), "auth-code", "st-2"); err != nil {
		t.Fatal(err)
	}
	acct, err := st.Get("evmsnipe@gmail.com")
	if err != nil {
		t.Fatalf("account not stored: %v", err)
	}
	if acct.AccessToken != jwtA || acct.Status != store.StatusActive || acct.DeviceID == "" {
		t.Errorf("acct = %+v", acct)
	}
	if s, _ := e.Status("st-2"); s.Status != "ok" || s.Email != "evmsnipe@gmail.com" {
		t.Errorf("session = %+v", s)
	}
}

func TestHandleCallback_UnknownState(t *testing.T) {
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := e.HandleCallback(context.Background(), "c", "does-not-exist"); err == nil {
		t.Error("expected error for unknown state")
	}
}
