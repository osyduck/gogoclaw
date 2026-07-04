package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogoclaw/internal/api"
	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/store"
)

// tok builds a minimal unsigned JWT whose payload decodes to the given claims.
func tok(jti string, exp int64) string {
	payload, _ := json.Marshal(map[string]any{"jti": jti, "exp": exp, "device_id": "dev"})
	return "Bearer h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

var jwtA = tok("evmsnipe@gmail.com", 1783192918)

// newServer wires a Server whose api.Client points at a mock AutoGLM.
func newServer(t *testing.T, autoglm http.HandlerFunc) (http.Handler, store.Store) {
	srv := httptest.NewServer(autoglm)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := api.NewClientWithBase(srv.URL)
	bus := events.New()
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus).Handler(), st
}

func TestLoginStart_ReturnsStateAndURL(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "https://g/o", "state": "st-1"}})
	})
	req := httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"manual"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["state"] != "st-1" || out["oauth_url"] != "https://g/o" {
		t.Errorf("out = %v", out)
	}
}

func TestCallback_CompletesLoginAndListsAccount(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/userapi/overseasv1/google-oauth-url":
			data = map[string]any{"oauth_url": "u", "state": "st-2"}
		case "/userapi/overseasv1/google-oauth-login":
			data = map[string]any{"access_token": jwtA, "refresh_token": jwtA, "user_id": "hexid"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	// start
	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"manual"}`)))
	// browser hits callback
	cb := httptest.NewRecorder()
	h.ServeHTTP(cb, httptest.NewRequest("GET", "/auth/callback-google?code=abc&state=st-2", nil))
	if cb.Code != 200 {
		t.Fatalf("callback code = %d", cb.Code)
	}
	// list shows the account and no raw token
	list := httptest.NewRecorder()
	h.ServeHTTP(list, httptest.NewRequest("GET", "/api/accounts", nil))
	body := list.Body.String()
	if !strings.Contains(body, "evmsnipe@gmail.com") {
		t.Errorf("account missing from list: %s", body)
	}
	if strings.Contains(body, jwtA) || strings.Contains(body, "Bearer ") {
		t.Errorf("list leaked a raw bearer token: %s", body)
	}
}

func TestDeleteAccount(t *testing.T) {
	h, st := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	_ = st.Add(store.Account{Email: "d@x.com", Status: store.StatusActive})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/accounts/d@x.com", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	if _, err := st.Get("d@x.com"); err == nil {
		t.Error("account not deleted")
	}
}

func TestServesPlaceholderUI(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "GogoClaw") {
		t.Errorf("placeholder UI not served: code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestSSE_DeliversPublishedEvent(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 256)
	// Read the ": connected" comment first — guarantees the subscription is live.
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("reading connect comment: %v", err)
	}
	// Now publish; the handler is subscribed, so this reaches it.
	// Reach the bus via a second account delete through the API.
	del := httptest.NewRecorder()
	h.ServeHTTP(del, httptest.NewRequest("DELETE", "/api/accounts/ghost@x.com", nil))

	buf2 := make([]byte, 256)
	n, err := resp.Body.Read(buf2)
	if err != nil {
		t.Fatalf("reading event: %v", err)
	}
	if !strings.Contains(string(buf2[:n]), "account:deleted") {
		t.Errorf("expected account:deleted event, got %q", string(buf2[:n]))
	}
}
