package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// aggResult is the fully-assembled result of an upstream OpenAI SSE stream,
// used to synthesize a single non-streaming response body.
type aggResult struct {
	ID           string
	Model        string
	Content      string
	Reasoning    string
	ToolCalls    []aggToolCall
	FinishReason string
	Usage        oaiUsage
	RawUsage     json.RawMessage // full upstream usage (cached_tokens, cost, …)
}

type aggToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// aggregateOpenAIStream reads an OpenAI SSE stream to completion and collects the
// assembled content, reasoning, tool calls, finish reason, and usage.
func aggregateOpenAIStream(r io.Reader) (aggResult, error) {
	var res aggResult
	byIndex := map[int]*aggToolCall{}
	var order []int

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk oaiChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.ID != "" {
			res.ID = chunk.ID
		}
		if u, ok := parseUsage(chunk.Usage); ok {
			res.Usage = u
			res.RawUsage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			d := ch.Delta
			res.Content += d.Content
			if d.Reasoning != "" {
				res.Reasoning += d.Reasoning
			} else {
				res.Reasoning += d.ReasoningContent
			}
			for _, tc := range d.ToolCalls {
				acc := byIndex[tc.Index]
				if acc == nil {
					acc = &aggToolCall{}
					byIndex[tc.Index] = acc
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				acc.Arguments += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				res.FinishReason = *ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	for _, idx := range order {
		res.ToolCalls = append(res.ToolCalls, *byIndex[idx])
	}
	return res, nil
}

// openAIResponse renders the aggregate as a single OpenAI chat.completion object.
func (a aggResult) openAIResponse(model string, created int64) map[string]any {
	msg := map[string]any{"role": "assistant"}
	if a.Content != "" || len(a.ToolCalls) == 0 {
		msg["content"] = a.Content
	} else {
		msg["content"] = nil
	}
	if len(a.ToolCalls) > 0 {
		var calls []map[string]any
		for _, tc := range a.ToolCalls {
			calls = append(calls, map[string]any{
				"id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
			})
		}
		msg["tool_calls"] = calls
	}
	finish := a.FinishReason
	if finish == "" {
		finish = "stop"
	}
	respModel := a.Model
	if respModel == "" {
		respModel = model
	}
	id := a.ID
	if id == "" {
		id = "chatcmpl-" + uuid()
	}
	// Preserve the full upstream usage object (cached_tokens, reasoning_tokens,
	// cost, …) verbatim; fall back to a minimal one only if it was absent.
	var usage any = a.RawUsage
	if len(a.RawUsage) == 0 {
		usage = map[string]int{
			"prompt_tokens": a.Usage.PromptTokens, "completion_tokens": a.Usage.CompletionTokens,
			"total_tokens": a.Usage.PromptTokens + a.Usage.CompletionTokens,
		}
	}
	return map[string]any{
		"id": id, "object": "chat.completion", "created": created, "model": respModel,
		"choices": []any{map[string]any{
			"index": 0, "message": msg, "finish_reason": finish,
		}},
		"usage": usage,
	}
}

// anthropicResponse renders the aggregate as a single Anthropic Messages object.
func (a aggResult) anthropicResponse(model string) map[string]any {
	var content []any
	if a.Reasoning != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": a.Reasoning})
	}
	if a.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": a.Content})
	}
	for _, tc := range a.ToolCalls {
		var input any = map[string]any{}
		if tc.Arguments != "" {
			var parsed any
			if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
				input = parsed
			}
		}
		id := tc.ID
		if id == "" {
			id = "tool_" + uuid()
		}
		content = append(content, map[string]any{
			"type": "tool_use", "id": id, "name": tc.Name, "input": input,
		})
	}
	if content == nil {
		content = []any{}
	}
	id := a.ID
	if id == "" {
		id = "msg_" + uuid()
	}
	usage := map[string]int{"input_tokens": a.Usage.PromptTokens, "output_tokens": a.Usage.CompletionTokens}
	if c := a.Usage.PromptTokensDetails.CachedTokens; c > 0 {
		usage["cache_read_input_tokens"] = c
	}
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": mapStopReason(a.FinishReason), "stop_sequence": nil,
		"usage": usage,
	}
}
