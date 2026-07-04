package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// handleChatCompletions implements POST /v1/chat/completions (OpenAI-compatible).
// It rewrites the friendly model to the bare id, sets X-Request-Model, forwards
// with account failover, and streams the SSE response through verbatim.
func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if !g.authOK(r) {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	// Preserve every field byte-exact except "model".
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var model string
	_ = json.Unmarshal(fields["model"], &model)
	route, ok := Lookup(model)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown model: "+model)
		return
	}
	// Whether the client wants a streamed response. The upstream is always
	// streamed; a non-streaming client gets the SSE aggregated into one JSON body.
	var clientStream bool
	_ = json.Unmarshal(fields["stream"], &clientStream)
	fields["model"], _ = json.Marshal(route.Bare)
	fields["stream"], _ = json.Marshal(true) // always stream upstream
	body, _ := json.Marshal(fields)

	resp, _, err := g.fwd.Forward(r.Context(), g.sel, route.Prefixed(), body)
	if err == ErrNoEligible {
		writeErr(w, http.StatusServiceUnavailable, "no eligible accounts")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || clientStream {
		streamThrough(w, resp) // stream (or surface an upstream error) verbatim
		return
	}
	agg, err := aggregateOpenAIStream(resp.Body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agg.openAIResponse(model, time.Now().Unix()))
}

// streamThrough copies an upstream SSE response to the client, flushing per read
// so tokens arrive incrementally.
func streamThrough(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
