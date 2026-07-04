package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAggregateTextStream(t *testing.T) {
	f, _ := os.Open("testdata/oai_text_stream.txt")
	defer f.Close()
	agg, err := aggregateOpenAIStream(f)
	if err != nil {
		t.Fatal(err)
	}
	if agg.Content == "" {
		t.Fatal("expected assembled content")
	}
	if agg.FinishReason != "stop" {
		t.Fatalf("finish = %q", agg.FinishReason)
	}
	// OpenAI response shape; the full upstream usage (incl. cached_tokens) survives.
	oai := agg.openAIResponse("glm-5.2", 123)
	b, _ := json.Marshal(oai)
	if !strings.Contains(string(b), `"object":"chat.completion"`) {
		t.Fatalf("oai response = %s", b)
	}
	if !strings.Contains(string(b), `"cached_tokens":21184`) {
		t.Fatalf("oai usage dropped cached_tokens: %s", b)
	}
	// Anthropic response shape (end_turn, text block, mapped cache_read tokens).
	an := agg.anthropicResponse("auto")
	b, _ = json.Marshal(an)
	if !strings.Contains(string(b), `"stop_reason":"end_turn"`) || !strings.Contains(string(b), `"type":"text"`) {
		t.Fatalf("anthropic response = %s", b)
	}
	if !strings.Contains(string(b), `"cache_read_input_tokens":21184`) {
		t.Fatalf("anthropic usage dropped cache_read_input_tokens: %s", b)
	}
}

func TestAggregateToolCallStream(t *testing.T) {
	f, _ := os.Open("testdata/oai_toolcall_stream.txt")
	defer f.Close()
	agg, err := aggregateOpenAIStream(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(agg.ToolCalls) == 0 {
		t.Fatal("expected assembled tool calls")
	}
	if agg.ToolCalls[0].Name == "" || agg.ToolCalls[0].Arguments == "" {
		t.Fatalf("tool call incomplete: %+v", agg.ToolCalls[0])
	}
	// Arguments must be valid JSON once assembled.
	var parsed any
	if err := json.Unmarshal([]byte(agg.ToolCalls[0].Arguments), &parsed); err != nil {
		t.Fatalf("assembled arguments not valid JSON: %q", agg.ToolCalls[0].Arguments)
	}
	if agg.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", agg.FinishReason)
	}
	an := agg.anthropicResponse("auto")
	b, _ := json.Marshal(an)
	if !strings.Contains(string(b), `"type":"tool_use"`) || !strings.Contains(string(b), `"stop_reason":"tool_use"`) {
		t.Fatalf("anthropic tool response = %s", string(b)[:min(400, len(b))])
	}
}

func TestChatCompletionsNonStreamingReturnsJSON(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/oai_text_stream.txt")
	gw := gatewayTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(fixture)
	})
	mux := http.NewServeMux()
	gw.Register(mux)
	rr := httptest.NewRecorder()
	// No "stream" field: client wants a single JSON body.
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var body struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if body.Object != "chat.completion" || len(body.Choices) != 1 || body.Choices[0].Message.Content == "" {
		t.Fatalf("aggregated body = %s", rr.Body.String())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
