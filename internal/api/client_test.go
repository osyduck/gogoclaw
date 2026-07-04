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

func TestGoogleOAuthURL(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/overseasv1/google-oauth-url" {
			t.Errorf("path = %s", path)
		}
		if body["source_id"] != "autoclaw" || body["device_id"] != "dev123" {
			t.Errorf("body = %v", body)
		}
		if body["navigate_uri"] != NavigateURI {
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
		if body["navigate_uri"] != NavigateURI {
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
