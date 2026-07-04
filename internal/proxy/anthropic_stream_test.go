package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// runTranslate runs the translator over a fixture and returns the ordered list
// of Anthropic event types plus the full output.
func runTranslate(t *testing.T, fixture string) (events []string, out string) {
	t.Helper()
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if err := translateOpenAIStream(&buf, func() {}, f, "auto"); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	return events, out
}

func TestTranslateTextStream(t *testing.T) {
	events, out := runTranslate(t, "testdata/oai_text_stream.txt")
	if events[0] != "message_start" {
		t.Fatalf("first event = %s", events[0])
	}
	if events[len(events)-1] != "message_stop" {
		t.Fatalf("last event = %s", events[len(events)-1])
	}
	mustContain(t, events, "content_block_start")
	mustContain(t, events, "content_block_delta")
	mustContain(t, events, "message_delta")
	if !strings.Contains(out, "text_delta") {
		t.Fatalf("no text_delta events emitted")
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected end_turn stop reason; out tail:\n%s", tail(out))
	}
}

func TestTranslateToolCallStream(t *testing.T) {
	_, out := runTranslate(t, "testdata/oai_toolcall_stream.txt")
	if !strings.Contains(out, `"type":"tool_use"`) {
		t.Fatalf("no tool_use content block emitted")
	}
	if !strings.Contains(out, "input_json_delta") {
		t.Fatalf("no input_json_delta events emitted")
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected tool_use stop reason; out tail:\n%s", tail(out))
	}
}

func TestMessagesEndpointEndToEnd(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/oai_text_stream.txt")
	gw := gatewayTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(fixture)
	})
	mux := http.NewServeMux()
	gw.Register(mux)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "event: message_start") {
		t.Fatalf("no message_start in response:\n%s", tail(rr.Body.String()))
	}
	if !strings.Contains(rr.Body.String(), "event: message_stop") {
		t.Fatalf("no message_stop in response")
	}
}

func mustContain(t *testing.T, xs []string, want string) {
	t.Helper()
	for _, x := range xs {
		if x == want {
			return
		}
	}
	t.Fatalf("events missing %q: %v", want, xs)
}

func tail(s string) string {
	if len(s) > 600 {
		return s[len(s)-600:]
	}
	return s
}
