package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogoclaw/internal/store"
)

func newGateway(t *testing.T) (*Gateway, store.Store) {
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, "http://upstream.invalid"), st
}

func serve(gw *Gateway) http.Handler {
	mux := http.NewServeMux()
	gw.Register(mux)
	return mux
}

func TestModelsEndpoint(t *testing.T) {
	gw, _ := newGateway(t)
	rr := httptest.NewRecorder()
	serve(gw).ServeHTTP(rr, httptest.NewRequest("GET", "/v1/models", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Object != "list" || len(body.Data) < 3 {
		t.Fatalf("models body = %s", rr.Body.String())
	}
}

func TestConfigRoundTrip(t *testing.T) {
	gw, _ := newGateway(t)
	h := serve(gw)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/proxy/config",
		strings.NewReader(`{"mode":"rotate_after_n","n":3,"api_key":"sk-test"}`))
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("post status %d: %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/proxy/config", nil))
	var got struct {
		Mode      string `json:"mode"`
		N         int    `json:"n"`
		APIKeySet bool   `json:"api_key_set"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Mode != "rotate_after_n" || got.N != 3 || !got.APIKeySet {
		t.Fatalf("config get = %+v", got)
	}
}

func TestProxyAuthRejectsWrongKey(t *testing.T) {
	gw, st := newGateway(t)
	st.SetProxyConfig(store.ProxyConfig{Mode: "sticky", N: 5, APIKey: "sk-secret"})
	h := serve(gw)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"auto"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestSetConfigBlankKeyKeepsExisting(t *testing.T) {
	gw, st := newGateway(t)
	st.SetProxyConfig(store.ProxyConfig{Mode: "sticky", N: 5, APIKey: "keepme"})
	h := serve(gw)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/proxy/config",
		strings.NewReader(`{"mode":"round_robin","n":2,"api_key":""}`)))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	got, _ := st.GetProxyConfig()
	if got.APIKey != "keepme" || got.Mode != "round_robin" {
		t.Fatalf("config = %+v, want key preserved + mode updated", got)
	}
}
