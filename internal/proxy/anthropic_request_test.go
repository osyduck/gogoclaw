package proxy

import (
	"encoding/json"
	"testing"
)

func TestTranslateAnthropicRequest_TextAndSystem(t *testing.T) {
	in := `{
	  "model":"auto","max_tokens":1024,"stream":true,
	  "system":"You are terse.",
	  "messages":[{"role":"user","content":"hi"}]
	}`
	out, model, stream, err := translateAnthropicRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if model != "auto" || !stream {
		t.Fatalf("model=%q stream=%v", model, stream)
	}
	var o struct {
		Model               string `json:"model"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		Stream              bool   `json:"stream"`
		Messages            []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &o); err != nil {
		t.Fatal(err)
	}
	if o.MaxCompletionTokens != 1024 || !o.Stream {
		t.Fatalf("out = %s", out)
	}
	if len(o.Messages) != 2 || o.Messages[0].Role != "system" || o.Messages[1].Role != "user" {
		t.Fatalf("messages = %s", out)
	}
}

func TestTranslateAnthropicRequest_ToolsAndToolResult(t *testing.T) {
	in := `{
	  "model":"auto","max_tokens":8,
	  "tools":[{"name":"read","description":"read file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],
	  "tool_choice":{"type":"auto"},
	  "messages":[
	    {"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"read","input":{"path":"a.txt"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"hello"}]}
	  ]
	}`
	out, _, _, err := translateAnthropicRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice any `json:"tool_choice"`
		Messages   []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
			Content    any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Tools) != 1 || o.Tools[0].Type != "function" || o.Tools[0].Function.Name != "read" {
		t.Fatalf("tools = %s", out)
	}
	if o.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %v", o.ToolChoice)
	}
	if len(o.Messages) != 2 || len(o.Messages[0].ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls missing: %s", out)
	}
	if o.Messages[0].ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("arguments = %q", o.Messages[0].ToolCalls[0].Function.Arguments)
	}
	if o.Messages[1].Role != "tool" || o.Messages[1].ToolCallID != "tu_1" {
		t.Fatalf("tool result msg = %s", out)
	}
}
