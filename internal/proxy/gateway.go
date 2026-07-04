package proxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"gogoclaw/internal/store"
)

// Gateway is the OpenAI/Anthropic-compatible LLM proxy surface.
type Gateway struct {
	st  store.Store
	sel *Selector
	fwd *Forwarder
}

// New builds a Gateway backed by store st, forwarding to upstreamBase.
func New(st store.Store, upstreamBase string) *Gateway {
	return &Gateway{st: st, sel: NewSelector(st), fwd: NewForwarder(upstreamBase)}
}

// Register mounts all gateway routes on mux.
func (g *Gateway) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", g.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("POST /v1/messages", g.handleMessages)
	mux.HandleFunc("GET /api/proxy/config", g.handleGetConfig)
	mux.HandleFunc("POST /api/proxy/config", g.handleSetConfig)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}

// authOK enforces the proxy API key when configured. Clients may present it via
// "Authorization: Bearer <key>" (OpenAI) or "x-api-key: <key>" (Anthropic).
func (g *Gateway) authOK(r *http.Request) bool {
	cfg, err := g.st.GetProxyConfig()
	if err != nil {
		return false // fail closed on store error
	}
	if cfg.APIKey == "" {
		return true // open when no key set
	}
	if r.Header.Get("x-api-key") == cfg.APIKey {
		return true
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return bearer == cfg.APIKey
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	var data []model
	for _, id := range Models() {
		data = append(data, model{ID: id, Object: "model", OwnedBy: "autoclaw"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := g.st.GetProxyConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	eligible := 0
	if list, err := g.sel.eligible(); err == nil {
		eligible = len(list)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": cfg.Mode, "n": cfg.N,
		"api_key_set":    cfg.APIKey != "",
		"eligible_count": eligible,
		"current":        g.sel.Current(),
	})
}

func (g *Gateway) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode   string `json:"mode"`
		N      int    `json:"n"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	switch req.Mode {
	case "sticky", "round_robin", "rotate_after_n":
	default:
		writeErr(w, http.StatusBadRequest, "mode must be sticky|round_robin|rotate_after_n")
		return
	}
	if req.N < 1 {
		req.N = 1
	}
	// A blank api_key means "keep the existing one" (the key is never echoed to
	// the client, so the UI can't round-trip it).
	key := req.APIKey
	if key == "" {
		if cur, err := g.st.GetProxyConfig(); err == nil {
			key = cur.APIKey
		}
	}
	if err := g.st.SetProxyConfig(store.ProxyConfig{Mode: req.Mode, N: req.N, APIKey: key}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- temporary stub; replaced in Task 8 by anthropic_stream.go ---

func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
	if !g.authOK(r) {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	writeErr(w, http.StatusNotImplemented, "not yet")
}
