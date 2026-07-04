package proxy

import (
	"encoding/json"
	"fmt"
)

// --- Anthropic request shapes (subset we translate) ---

type anthReq struct {
	Model       string          `json:"model"`
	System      json.RawMessage `json:"system"` // string OR []anthBlock
	Messages    []anthMessage   `json:"messages"`
	Tools       []anthTool      `json:"tools"`
	ToolChoice  json.RawMessage `json:"tool_choice"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
	Stream      bool            `json:"stream"`
}

type anthMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string OR []anthBlock
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // string OR []anthBlock (we stringify)
	// image
	Source *anthImageSource `json:"source"`
}

type anthImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// --- OpenAI target shapes ---

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Function oaiFunc `json:"function"`
}

type oaiFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// translateAnthropicRequest converts an Anthropic Messages request body into an
// OpenAI chat-completions body. The returned model is the friendly name (the
// caller resolves it via the catalog); stream reports the requested mode.
func translateAnthropicRequest(raw []byte) (out []byte, model string, stream bool, err error) {
	var in anthReq
	if err = json.Unmarshal(raw, &in); err != nil {
		return nil, "", false, fmt.Errorf("invalid anthropic request: %w", err)
	}

	msgs := make([]oaiMessage, 0, len(in.Messages)+1)

	// system → leading system message
	if len(in.System) > 0 {
		sys, serr := anthTextToString(in.System)
		if serr != nil {
			return nil, "", false, serr
		}
		if sys != "" {
			msgs = append(msgs, oaiMessage{Role: "system", Content: sys})
		}
	}

	for _, m := range in.Messages {
		translated, terr := translateMessage(m)
		if terr != nil {
			return nil, "", false, terr
		}
		msgs = append(msgs, translated...)
	}

	body := map[string]any{
		"model":    in.Model,
		"messages": msgs,
		"stream":   true, // we always stream upstream
	}
	if in.MaxTokens > 0 {
		body["max_completion_tokens"] = in.MaxTokens
	}
	if in.Temperature != nil {
		body["temperature"] = *in.Temperature
	}
	if len(in.Tools) > 0 {
		tools := make([]oaiTool, 0, len(in.Tools))
		for _, t := range in.Tools {
			var ot oaiTool
			ot.Type = "function"
			ot.Function.Name = t.Name
			ot.Function.Description = t.Description
			ot.Function.Parameters = t.InputSchema
			tools = append(tools, ot)
		}
		body["tools"] = tools
	}
	if tc := translateToolChoice(in.ToolChoice); tc != nil {
		body["tool_choice"] = tc
	}

	out, err = json.Marshal(body)
	return out, in.Model, in.Stream, err
}

// translateMessage converts one Anthropic message to one or more OpenAI messages.
func translateMessage(m anthMessage) ([]oaiMessage, error) {
	// content may be a plain string.
	var asString string
	if json.Unmarshal(m.Content, &asString) == nil {
		return []oaiMessage{{Role: m.Role, Content: asString}}, nil
	}
	var blocks []anthBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("bad content for role %s: %w", m.Role, err)
	}

	var out []oaiMessage
	var parts []any // multimodal parts for a user/assistant text message
	var toolCalls []oaiToolCall
	var textBuf string

	flushText := func() {
		if textBuf != "" || len(parts) > 0 {
			if len(parts) > 0 {
				if textBuf != "" {
					parts = append(parts, map[string]any{"type": "text", "text": textBuf})
				}
				out = append(out, oaiMessage{Role: m.Role, Content: parts})
			} else {
				out = append(out, oaiMessage{Role: m.Role, Content: textBuf})
			}
			textBuf, parts = "", nil
		}
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			textBuf += b.Text
		case "image":
			if b.Source != nil {
				url := fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
				parts = append(parts, map[string]any{
					"type": "image_url", "image_url": map[string]string{"url": url},
				})
			}
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, oaiToolCall{
				ID: b.ID, Type: "function",
				Function: oaiFunc{Name: b.Name, Arguments: args},
			})
		case "tool_result":
			flushText()
			content, err := anthTextToString(b.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, oaiMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
		}
	}
	flushText()
	if len(toolCalls) > 0 {
		out = append(out, oaiMessage{Role: m.Role, ToolCalls: toolCalls})
	}
	if len(out) == 0 {
		out = append(out, oaiMessage{Role: m.Role, Content: ""})
	}
	return out, nil
}

// anthTextToString flattens an Anthropic string-or-blocks value to plain text.
func anthTextToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []anthBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("expected string or text blocks: %w", err)
	}
	var out string
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			out += b.Text
		}
	}
	return out, nil
}

// translateToolChoice maps Anthropic tool_choice to OpenAI's form.
func translateToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		return map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
	}
	return nil
}
