package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

// failDriver simulates the stealth sidecar reporting a failed login.
type failDriver struct{ err error }

func (f failDriver) Drive(context.Context, api.Provider, string, *GoogleCred, func(string)) error {
	return f.err
}

// stepDriver reports progress steps then succeeds, exercising step recording.
type stepDriver struct{ steps []string }

func (d stepDriver) Drive(_ context.Context, _ api.Provider, _ string, _ *GoogleCred, onStep func(string)) error {
	for _, s := range d.steps {
		onStep(s)
	}
	return nil
}

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
	state, url, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, nil)
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
		case "/agent-assetmgr/api/v2/wallets":
			data = map[string]any{"total_balance": 1500}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, nil); err != nil {
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
	if acct.Balance != 1500 {
		t.Errorf("balance = %d, want 1500 seeded on login", acct.Balance)
	}
	if s, _ := e.Status("st-2"); s.Status != "ok" || s.Email != "evmsnipe@gmail.com" {
		t.Errorf("session = %+v", s)
	}
}

func TestHandleCallback_IdempotentOnReload(t *testing.T) {
	loginCalls := 0
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/userapi/overseasv1/google-oauth-url":
			data = map[string]any{"oauth_url": "u", "state": "st-3"}
		case "/userapi/overseasv1/google-oauth-login":
			loginCalls++
			data = map[string]any{"access_token": jwtA, "refresh_token": jwtA, "user_id": "hexid", "user_name": "EVM"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.HandleCallback(context.Background(), "auth-code", "st-3"); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.Status("st-3"); s.Status != "ok" {
		t.Fatalf("session after first callback = %+v", s)
	}

	// Simulate the browser reloading the callback URL: same state, same
	// (now-consumed) code. This must short-circuit rather than re-exchange.
	if err := e.HandleCallback(context.Background(), "auth-code", "st-3"); err != nil {
		t.Errorf("second callback returned error: %v", err)
	}
	if s, _ := e.Status("st-3"); s.Status != "ok" {
		t.Errorf("session status flipped after reload: %+v", s)
	}
	if loginCalls != 1 {
		t.Errorf("google-oauth-login called %d times, want 1", loginCalls)
	}
}

// TestStartLogin_AutoFailureAttributesEmail guards that a driver (stealth
// sidecar) failure marks the session as errored with the credential's email
// AND publishes a login:error event carrying that email — so the bulk UI can
// show which account failed and why, instead of an unattributed error.
func TestStartLogin_AutoFailureAttributesEmail(t *testing.T) {
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-f"}})
	})
	ch, unsub := e.bus.Subscribe()
	defer unsub()

	cred := &GoogleCred{Email: "bulk@x.com", Password: "pw"}
	state, _, err := e.StartLogin(context.Background(), failDriver{err: errors.New("stealth login failed: blocked")}, api.ProviderZai, cred)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "login:error" || ev.Email != "bulk@x.com" {
			t.Errorf("event = %+v, want login:error for bulk@x.com", ev)
		}
		if ev.Detail == "" {
			t.Error("event detail (failure reason) is empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no login:error event published")
	}

	s, ok := e.Status(state)
	if !ok || s.Status != "error" || s.Email != "bulk@x.com" {
		t.Errorf("session = %+v ok=%v, want errored session for bulk@x.com", s, ok)
	}
	if s.Err == "" {
		t.Error("session error reason is empty")
	}
}

// TestHandleCallback_ZaiUsesZaiLoginEndpoint guards that a session started with
// the zai provider completes via the zai-oauth-login endpoint (not google's).
func TestHandleCallback_ZaiUsesZaiLoginEndpoint(t *testing.T) {
	var loginPath string
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/userapi/overseasv1/zai-oauth-url":
			data = map[string]any{"oauth_url": "https://chat.z.ai/x", "state": "st-z"}
		case "/userapi/overseasv1/zai-oauth-login":
			loginPath = r.URL.Path
			data = map[string]any{"access_token": jwtA, "refresh_token": jwtA, "user_id": "hexid"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderZai, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.HandleCallback(context.Background(), "code-z", "st-z"); err != nil {
		t.Fatal(err)
	}
	if loginPath != "/userapi/overseasv1/zai-oauth-login" {
		t.Errorf("login path = %q, want zai endpoint", loginPath)
	}
	if _, err := st.Get("evmsnipe@gmail.com"); err != nil {
		t.Errorf("account not persisted: %v", err)
	}
}

// TestStartLogin_RecordsDriverSteps guards that per-action steps reported by the
// driver are recorded on the session so the UI can render a live terminal.
func TestStartLogin_RecordsDriverSteps(t *testing.T) {
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-s"}})
	})
	drv := stepDriver{steps: []string{"[  0.0s] launch", "[  1.0s] enter email"}}
	state, _, err := e.StartLogin(context.Background(), drv, api.ProviderGoogle, &GoogleCred{Email: "a@x.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	// Steps are appended from the Drive goroutine; poll briefly.
	var s Session
	for i := 0; i < 100; i++ {
		s, _ = e.Status(state)
		if len(s.Steps) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(s.Steps) != 2 || !strings.Contains(s.Steps[1], "enter email") {
		t.Errorf("session steps = %v, want the two driver steps", s.Steps)
	}
}

func TestTryVia_NoPoolCallsPlainClientOnce(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	var attempts int
	err := e.tryVia(nil, nil, func(*api.Client) error { attempts++; return nil })
	if err != nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v, want 1,nil", attempts, err)
	}
}

func TestTryVia_FailsOverOn630014(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	order := []string{"http://p1:8080", "http://p2:8080", "http://p3:8080"}
	var attempts int
	err := e.tryVia(order, nil, func(*api.Client) error {
		attempts++
		if attempts < 2 {
			return &api.APIError{Code: api.CodeVerificationFailed, Msg: "Verification failed"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want success after one failover, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts=%d, want 2", attempts)
	}
}

func TestTryVia_TerminalErrorStopsImmediately(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	order := []string{"http://p1:8080", "http://p2:8080"}
	var attempts int
	err := e.tryVia(order, nil, func(*api.Client) error {
		attempts++
		return &api.APIError{Code: 400001, Msg: "bad request"}
	})
	if err == nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v, want 1 and terminal error", attempts, err)
	}
}

func TestTryVia_ExhaustsPoolThenReturnsLastError(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	order := []string{"http://p1:8080", "http://p2:8080"}
	var attempts int
	err := e.tryVia(order, nil, func(*api.Client) error {
		attempts++
		return &api.APIError{Code: api.CodeVerificationFailed, Msg: "Verification failed"}
	})
	if err == nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v, want 2 and the last error", attempts, err)
	}
}

func TestNextProxyOrder_RoundRobinRotation(t *testing.T) {
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := st.SetLoginProxies([]string{"http://a:1", "http://b:2", "http://c:3"}); err != nil {
		t.Fatal(err)
	}
	first := e.nextProxyOrder()
	second := e.nextProxyOrder()
	if len(first) != 3 || first[0] != "http://a:1" {
		t.Fatalf("first order = %v", first)
	}
	if len(second) != 3 || second[0] != "http://b:2" {
		t.Fatalf("second order = %v, want to start at the next proxy", second)
	}
}

// TestStartLogin_ProxyPoolRoutesThroughProxy proves the toggle actually routes
// AutoGLM calls through the pool: with a single unreachable proxy, the OAuthURL
// call fails (so StartLogin errors), whereas the same flow with the pool off
// succeeds via the direct base server.
func TestStartLogin_ProxyPoolRoutesThroughProxy(t *testing.T) {
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-p"}})
	})
	if err := st.SetLoginProxies([]string{"http://127.0.0.1:1"}); err != nil { // refused
		t.Fatal(err)
	}

	// Pool ON → routed through the dead proxy → StartLogin fails.
	on := &GoogleCred{Email: "a@x.com", Password: "pw", UseProxyPool: true}
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, on); err == nil {
		t.Error("want error when routing through an unreachable proxy")
	}

	// Pool OFF → direct base server → StartLogin succeeds.
	off := &GoogleCred{Email: "b@x.com", Password: "pw", UseProxyPool: false}
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, off); err != nil {
		t.Errorf("pool off should use the direct base: %v", err)
	}
}

func TestHandleCallback_UnknownState(t *testing.T) {
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := e.HandleCallback(context.Background(), "c", "does-not-exist"); err == nil {
		t.Error("expected error for unknown state")
	}
}
