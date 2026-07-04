package server

import (
	"context"
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
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus, nil).Handler(), st
}

// fakeAutoLogin drives logins through a stub driver without a real sidecar.
type fakeAutoLogin struct {
	ensured bool
	driver  auth.LoginDriver
}

func (f *fakeAutoLogin) Ensure(context.Context) error { f.ensured = true; return nil }
func (f *fakeAutoLogin) Driver() auth.LoginDriver     { return f.driver }

// stubDriver is a LoginDriver that succeeds immediately (no browser).
type stubDriver struct{}

func (stubDriver) Drive(context.Context, api.Provider, string, *auth.GoogleCred) error { return nil }

func newServerWithAuto(t *testing.T, autoglm http.HandlerFunc, al AutoLogin) http.Handler {
	srv := httptest.NewServer(autoglm)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := api.NewClientWithBase(srv.URL)
	bus := events.New()
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus, al).Handler()
}

func TestCallbackZaiRouteExists(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": map[string]any{}})
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/callback-zai?code=c&state=nope", nil))
	// Unknown state → 400 (not 404): the route is wired to the callback handler.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("callback-zai code = %d, want 400 (routed)", rec.Code)
	}
}

func TestLoginStart_RejectsUnknownProvider(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": map[string]any{}})
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"manual","provider":"evil"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown provider code = %d, want 400", rec.Code)
	}
}

func TestLoginStart_AutoUsesSidecarWhenConfigured(t *testing.T) {
	al := &fakeAutoLogin{driver: stubDriver{}}
	h := newServerWithAuto(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-a"}})
	}, al)
	rec := httptest.NewRecorder()
	body := `{"mode":"auto","cred":{"Email":"a@x.com","Password":"pw"}}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/start", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	if !al.ensured {
		t.Error("Ensure was not called for auto login")
	}
}

func TestBulkLogin_StartsEachAccount(t *testing.T) {
	al := &fakeAutoLogin{driver: stubDriver{}}
	var n int
	h := newServerWithAuto(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-" + strings.Repeat("x", n)}})
	}, al)
	rec := httptest.NewRecorder()
	body := `{"accounts":[{"email":"a@x.com","password":"p1"},{"email":"b@x.com","password":"p2"}]}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/bulk", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	var out struct {
		Started []struct {
			Email string `json:"email"`
			State string `json:"state"`
		} `json:"started"`
		Errors []struct {
			Email string `json:"email"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Started) != 2 {
		t.Errorf("started = %d, want 2 (%s)", len(out.Started), rec.Body)
	}
	if out.Errors == nil {
		t.Errorf("errors decoded as nil, want empty non-nil slice (%s)", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"errors":[]`) {
		t.Errorf("body = %s, want errors field to marshal as [] not null", rec.Body)
	}
}

func TestBulkLogin_501WhenUnconfigured(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	body := `{"accounts":[{"email":"a@x.com","password":"p1"}]}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/bulk", strings.NewReader(body)))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("code = %d, want 501 when auto-login unconfigured", rec.Code)
	}
}

func TestBulkLogin_400OnEmptyAccounts(t *testing.T) {
	al := &fakeAutoLogin{driver: stubDriver{}}
	h := newServerWithAuto(t, func(w http.ResponseWriter, r *http.Request) {}, al)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/bulk", strings.NewReader(`{"accounts":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for empty accounts", rec.Code)
	}
}

func TestLoginStart_AutoStill501WhenUnconfigured(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"auto"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("code = %d, want 501 when auto-login unconfigured", rec.Code)
	}
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

func TestLoginStart_AutoReturns501(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"auto"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("auto mode: code = %d, want 501", rec.Code)
	}
}

func TestRefreshOne_MissingAccountReturns404(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/accounts/ghost@x.com/refresh", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing account refresh: code = %d, want 404", rec.Code)
	}
}

func TestLoginStatus_UnknownState(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/login/status?state=nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown state: code = %d, want 404", rec.Code)
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
