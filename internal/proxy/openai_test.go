package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gogoclaw/internal/store"
)

// gatewayTo builds a Gateway whose upstream is a caller-supplied handler, with
// one eligible account seeded.
func gatewayTo(t *testing.T, upstream http.HandlerFunc) *Gateway {
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.Add(store.Account{
		Email: "a@x.com", UserID: "1", DeviceID: "d",
		AccessToken: "JWT", RefreshToken: "R",
		AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(time.Hour),
		PrivPEM: "p", PubPEM: "P", AddedAt: time.Now(), LastRefreshedAt: time.Now(),
		Status: store.StatusActive, Balance: 100,
	})
	// Add() does not persist balance; set it explicitly so the account is eligible.
	st.UpdateBalance("a@x.com", 100)
	st.SetProxyConfig(store.ProxyConfig{Mode: "sticky", N: 5})
	return New(st, up.URL)
}

func TestChatCompletionsRewritesModelAndStreams(t *testing.T) {
	var gotModelHeader, gotBodyModel string
	gw := gatewayTo(t, func(w http.ResponseWriter, r *http.Request) {
		gotModelHeader = r.Header.Get("X-Request-Model")
		var body struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		gotBodyModel = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	})

	mux := http.NewServeMux()
	gw.Register(mux)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	mux.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if gotModelHeader != "openrouter_glm-5.2" {
		t.Errorf("X-Request-Model = %q", gotModelHeader)
	}
	if gotBodyModel != "glm-5.2" {
		t.Errorf("body model = %q, want bare glm-5.2", gotBodyModel)
	}
	if !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Errorf("stream not passed through: %s", rr.Body.String())
	}
}

func TestChatCompletionsUnknownModel(t *testing.T) {
	gw := gatewayTo(t, func(w http.ResponseWriter, r *http.Request) {})
	mux := http.NewServeMux()
	gw.Register(mux)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"mystery"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
