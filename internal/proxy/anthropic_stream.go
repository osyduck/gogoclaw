package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// --- OpenAI streaming chunk shapes (subset) ---

type oaiChunk struct {
	ID      string          `json:"id"`
	Choices []oaiChoice     `json:"choices"`
	Usage   json.RawMessage `json:"usage"` // kept raw to preserve cached_tokens etc.
}

type oaiChoice struct {
	Delta        oaiDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

type oaiDelta struct {
	Content          string             `json:"content"`
	Reasoning        string             `json:"reasoning"`
	ReasoningContent string             `json:"reasoning_content"`
	ToolCalls        []oaiToolCallDelta `json:"tool_calls"`
}

type oaiToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// parseUsage decodes a raw upstream usage object, reporting whether it was present.
func parseUsage(raw json.RawMessage) (oaiUsage, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return oaiUsage{}, false
	}
	var u oaiUsage
	if json.Unmarshal(raw, &u) != nil {
		return oaiUsage{}, false
	}
	return u, true
}

// blockKind is the currently-open Anthropic content block type.
type blockKind int

const (
	none blockKind = iota
	textBlock
	thinkingBlock
	toolBlock
)

// streamState tracks the open Anthropic content block while translating.
type streamState struct {
	w          io.Writer
	flush      func()
	nextIndex  int       // next Anthropic content-block index to assign
	openKind   blockKind // kind of the currently open block (none if closed)
	openIndex  int       // index of the currently open block
	openTool   int       // OpenAI tool_calls index mapped to the open tool block
	stopReason string
	usage      oaiUsage
}

// emit writes one Anthropic SSE event.
func (s *streamState) emit(event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	s.flush()
}

func (s *streamState) closeBlock() {
	if s.openKind == none {
		return
	}
	s.emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.openIndex})
	s.openKind = none
}

func (s *streamState) openText() {
	if s.openKind == textBlock {
		return
	}
	s.closeBlock()
	s.openIndex, s.openKind = s.nextIndex, textBlock
	s.nextIndex++
	s.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.openIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (s *streamState) openThinking() {
	if s.openKind == thinkingBlock {
		return
	}
	s.closeBlock()
	s.openIndex, s.openKind = s.nextIndex, thinkingBlock
	s.nextIndex++
	s.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.openIndex,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	})
}

func (s *streamState) openToolBlock(idx int, id, name string) {
	if s.openKind == toolBlock && s.openTool == idx {
		return
	}
	s.closeBlock()
	s.openIndex, s.openKind, s.openTool = s.nextIndex, toolBlock, idx
	s.nextIndex++
	if id == "" {
		id = fmt.Sprintf("tool_%d", idx)
	}
	s.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.openIndex,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
	})
}

// translateOpenAIStream reads an OpenAI SSE stream and writes the equivalent
// Anthropic Messages SSE event stream.
func translateOpenAIStream(w io.Writer, flush func(), r io.Reader, model string) error {
	s := &streamState{w: w, flush: flush, stopReason: "end_turn"}

	// message_start
	s.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_" + uuid(), "type": "message", "role": "assistant",
			"model": model, "content": []any{}, "stop_reason": nil,
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024) // large SSE lines
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // SSE comments (": OPENROUTER PROCESSING") and blanks
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk oaiChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // skip malformed chunk
		}
		if u, ok := parseUsage(chunk.Usage); ok {
			s.usage = u
		}
		for _, ch := range chunk.Choices {
			s.applyDelta(ch.Delta)
			if ch.FinishReason != nil {
				s.stopReason = mapStopReason(*ch.FinishReason)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	s.closeBlock()
	usage := map[string]int{
		"input_tokens":  s.usage.PromptTokens,
		"output_tokens": s.usage.CompletionTokens,
	}
	if c := s.usage.PromptTokensDetails.CachedTokens; c > 0 {
		usage["cache_read_input_tokens"] = c
	}
	s.emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": s.stopReason, "stop_sequence": nil},
		"usage": usage,
	})
	s.emit("message_stop", map[string]any{"type": "message_stop"})
	return nil
}

// applyDelta emits the Anthropic events for one OpenAI delta.
func (s *streamState) applyDelta(d oaiDelta) {
	// Reasoning first (thinking block).
	reason := d.Reasoning
	if reason == "" {
		reason = d.ReasoningContent
	}
	if reason != "" {
		s.openThinking()
		s.emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.openIndex,
			"delta": map[string]any{"type": "thinking_delta", "thinking": reason},
		})
	}
	if d.Content != "" {
		s.openText()
		s.emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.openIndex,
			"delta": map[string]any{"type": "text_delta", "text": d.Content},
		})
	}
	for _, tc := range d.ToolCalls {
		s.openToolBlock(tc.Index, tc.ID, tc.Function.Name)
		if tc.Function.Arguments != "" {
			s.emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": s.openIndex,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
			})
		}
	}
}

// mapStopReason maps an OpenAI finish_reason to an Anthropic stop_reason.
func mapStopReason(fr string) string {
	switch fr {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// handleMessages implements POST /v1/messages (Anthropic-compatible). It
// translates the request to OpenAI form, forwards with failover, and translates
// the streamed OpenAI response back into Anthropic SSE events.
func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
	if !g.authOK(r) {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	oaiBody, model, clientStream, err := translateAnthropicRequest(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	route, ok := Lookup(model)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown model: "+model)
		return
	}
	// Rewrite the body's model to the bare id (translateAnthropicRequest kept the
	// friendly name).
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(oaiBody, &fields)
	fields["model"], _ = json.Marshal(route.Bare)
	oaiBody, _ = json.Marshal(fields)

	resp, _, err := g.fwd.Forward(r.Context(), g.sel, route.Prefixed(), oaiBody)
	if err == ErrNoEligible {
		writeErr(w, http.StatusServiceUnavailable, "no eligible accounts")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		streamThrough(w, resp) // surface upstream error without translation
		return
	}
	if !clientStream {
		// Non-streaming client (generateText): aggregate into one Messages body.
		agg, aerr := aggregateOpenAIStream(resp.Body)
		if aerr != nil {
			writeErr(w, http.StatusBadGateway, aerr.Error())
			return
		}
		writeJSON(w, http.StatusOK, agg.anthropicResponse(model))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := translateOpenAIStream(w, flush, resp.Body, model); err != nil {
		fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":%q}}\n\n", err.Error())
		flush()
	}
}
