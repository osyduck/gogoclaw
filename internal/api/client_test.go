package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newTestClient spins up a mock AutoGLM server; handler receives (path, body, authorization header).
func newTestClient(t *testing.T, handler func(path string, body map[string]any, auth string) any) (*Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-auth-sign") == "" || r.Header.Get("x-auth-appid") != "100003" {
			t.Errorf("missing signing headers on %s", r.URL.Path)
		}
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		data := handler(r.URL.Path, body, r.Header.Get("authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	}))
	return NewClientWithBase(srv.URL), srv.Close
}

// newRawTestClient spins up a mock server that always responds with the
// given status code and raw body, ignoring signing/auth entirely — used to
// exercise postSigned's error path when the response isn't a valid envelope
// (e.g. an auth gateway or proxy returning HTML/empty body on 401/407/5xx).
func newRawTestClient(t *testing.T, status int, rawBody string) (*Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(rawBody))
	}))
	return NewClientWithBase(srv.URL), srv.Close
}

// TestPostSigned_WrapsHTTPStatusOnDecodeFailure guards the fix that surfaces
// the HTTP status code when decodeEnvelope fails, so callers (e.g. a
// background refresher) can distinguish auth failures (401/407) from
// transient server errors (5xx) instead of seeing an opaque decode error.
func TestPostSigned_WrapsHTTPStatusOnDecodeFailure(t *testing.T) {
	c, done := newRawTestClient(t, http.StatusInternalServerError, "<html>error</html>")
	defer done()
	_, _, err := c.GoogleOAuthURL(context.Background(), "dev123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusInternalServerError)) {
		t.Errorf("error = %q, want it to contain %d", err.Error(), http.StatusInternalServerError)
	}
}

func TestWithProxy_SetsProxiedTransport(t *testing.T) {
	c := NewClient()
	pc, err := c.WithProxy("http://user:pass@127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := pc.http.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("expected an *http.Transport with a Proxy func")
	}
	u, err := tr.Proxy(httptest.NewRequest("GET", "http://autoglm-api.autoglm.ai/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "127.0.0.1:8080" {
		t.Errorf("proxy = %v, want host 127.0.0.1:8080", u)
	}
}

func TestWithProxy_EmptyReturnsReceiver(t *testing.T) {
	c := NewClient()
	pc, err := c.WithProxy("")
	if err != nil || pc != c {
		t.Errorf("empty proxy: got (%p,%v), want receiver unchanged", pc, err)
	}
}

func TestWithProxy_InvalidURLErrors(t *testing.T) {
	c := NewClient()
	if _, err := c.WithProxy("http://%zz"); err == nil {
		t.Error("expected error for malformed proxy url")
	}
}

func TestGoogleOAuthURL(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/overseasv1/google-oauth-url" {
			t.Errorf("path = %s", path)
		}
		if body["source_id"] != "autoclaw" || body["device_id"] != "dev123" {
			t.Errorf("body = %v", body)
		}
		if body["navigate_uri"] != NavigateURIFor(ProviderGoogle) {
			t.Errorf("navigate_uri = %v", body["navigate_uri"])
		}
		if auth != "" {
			t.Errorf("authorization = %q, want empty", auth)
		}
		return map[string]any{"oauth_url": "https://accounts.google.com/x", "state": "st1"}
	})
	defer done()
	url, state, err := c.GoogleOAuthURL(context.Background(), "dev123")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://accounts.google.com/x" || state != "st1" {
		t.Errorf("got %s / %s", url, state)
	}
}

func TestGoogleOAuthLogin(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/overseasv1/google-oauth-login" {
			t.Errorf("path = %s", path)
		}
		if body["code"] != "auth-code" || body["state"] != "st1" {
			t.Errorf("body = %v", body)
		}
		if body["navigate_uri"] != NavigateURIFor(ProviderGoogle) {
			t.Errorf("navigate_uri = %v", body["navigate_uri"])
		}
		if auth != "" {
			t.Errorf("authorization = %q, want empty", auth)
		}
		return map[string]any{
			"access_token": "Bearer aaa", "refresh_token": "Bearer rrr",
			"user_id": "hexuser", "user_name": "EVM Snipe", "first_login": true,
		}
	})
	defer done()
	res, err := c.GoogleOAuthLogin(context.Background(), "dev123", "auth-code", "st1")
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken != "Bearer aaa" || res.RefreshToken != "Bearer rrr" || !res.FirstLogin {
		t.Errorf("res = %+v", res)
	}
}

func TestRefresh(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/v1/refresh" {
			t.Errorf("path = %s", path)
		}
		if body["refresh_token"] != "Bearer rrr" {
			t.Errorf("body = %v", body)
		}
		if auth != "Bearer old-a" {
			t.Errorf("authorization = %q, want %q", auth, "Bearer old-a")
		}
		return map[string]any{"access_token": "Bearer new-a", "refresh_token": "Bearer new-r", "refresh": false}
	})
	defer done()
	access, refresh, err := c.Refresh(context.Background(), "dev123", "Bearer old-a", "Bearer rrr")
	if err != nil {
		t.Fatal(err)
	}
	if access != "Bearer new-a" || refresh != "Bearer new-r" {
		t.Errorf("got %s / %s", access, refresh)
	}
}

func TestRefresh_KeepsOldRefreshTokenWhenResponseEmpty(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		return map[string]any{"access_token": "Bearer new-a", "refresh_token": "", "refresh": false}
	})
	defer done()
	_, refresh, err := c.Refresh(context.Background(), "dev123", "Bearer old-a", "Bearer keep-r")
	if err != nil {
		t.Fatal(err)
	}
	if refresh != "Bearer keep-r" {
		t.Errorf("refresh = %q, want fallback to input", refresh)
	}
}

func TestOAuthURL_ZaiUsesZaiPathAndCallback(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/overseasv1/zai-oauth-url" {
			t.Errorf("path = %s", path)
		}
		if body["navigate_uri"] != "http://localhost:18432/auth/callback-zai" {
			t.Errorf("navigate_uri = %v", body["navigate_uri"])
		}
		return map[string]any{"oauth_url": "https://chat.z.ai/x", "state": "st-z"}
	})
	defer done()
	url, state, err := c.OAuthURL(context.Background(), ProviderZai, "dev1")
	if err != nil || url != "https://chat.z.ai/x" || state != "st-z" {
		t.Fatalf("got %q/%q err=%v", url, state, err)
	}
}

func TestOAuthLogin_ZaiUsesZaiPath(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/overseasv1/zai-oauth-login" {
			t.Errorf("path = %s", path)
		}
		return map[string]any{"access_token": "Bearer a", "refresh_token": "Bearer r", "user_id": "u"}
	})
	defer done()
	res, err := c.OAuthLogin(context.Background(), ProviderZai, "dev1", "code-1", "st-z")
	if err != nil || res.AccessToken != "Bearer a" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestParseProvider(t *testing.T) {
	for in, want := range map[string]Provider{"": ProviderGoogle, "google": ProviderGoogle, "zai": ProviderZai} {
		if got, err := ParseProvider(in); err != nil || got != want {
			t.Errorf("ParseProvider(%q) = %q,%v want %q", in, got, err, want)
		}
	}
	if _, err := ParseProvider("evil/../path"); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestWallets(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/agent-assetmgr/api/v2/wallets" {
			t.Errorf("path = %s", path)
		}
		if auth != "Bearer aaa" {
			t.Errorf("authorization = %q, want %q", auth, "Bearer aaa")
		}
		return map[string]any{
			"total_balance": 2300,
			"wallets": []any{
				map[string]any{"public_wallet_type": "reward", "display_name": "Reward Points", "balance": 2300, "display": true},
				map[string]any{"public_wallet_type": "daily", "display_name": "Daily Points", "balance": 0, "display": false},
			},
		}
	})
	defer done()
	w, err := c.Wallets(context.Background(), "Bearer aaa")
	if err != nil {
		t.Fatal(err)
	}
	if w.TotalBalance != 2300 {
		t.Errorf("total = %d, want 2300", w.TotalBalance)
	}
	if len(w.Wallets) != 2 || w.Wallets[0].Type != "reward" || w.Wallets[0].Balance != 2300 {
		t.Errorf("wallets = %+v", w.Wallets)
	}
}

func TestUserProfile(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/v1/user-profile" {
			t.Errorf("path = %s", path)
		}
		if auth != "Bearer aaa" {
			t.Errorf("authorization = %q, want %q", auth, "Bearer aaa")
		}
		return map[string]any{"email": "evmsnipe@gmail.com", "user_name": "EVM Snipe", "user_id": "hexuser"}
	})
	defer done()
	p, err := c.UserProfile(context.Background(), "dev123", "Bearer aaa")
	if err != nil {
		t.Fatal(err)
	}
	if p.Email != "evmsnipe@gmail.com" {
		t.Errorf("profile = %+v", p)
	}
}
