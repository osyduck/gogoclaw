package api

import (
	"strings"
	"testing"
)

func TestDecodeEnvelope_Success(t *testing.T) {
	body := `{"code":0,"msg":"SUCCESS","data":{"oauth_url":"https://x","state":"abc"}}`
	var out struct {
		OAuthURL string `json:"oauth_url"`
		State    string `json:"state"`
	}
	if err := decodeEnvelope(strings.NewReader(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.OAuthURL != "https://x" || out.State != "abc" {
		t.Errorf("decoded = %+v", out)
	}
}

func TestDecodeEnvelope_APIError(t *testing.T) {
	body := `{"code":40001,"msg":"bad request","data":null}`
	err := decodeEnvelope(strings.NewReader(body), nil)
	if err == nil || !strings.Contains(err.Error(), "40001") {
		t.Errorf("expected error containing code, got %v", err)
	}
}
