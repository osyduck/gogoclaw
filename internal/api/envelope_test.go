package api

import (
	"errors"
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

func TestDecodeEnvelope_ReturnsTypedAPIError(t *testing.T) {
	body := `{"code":40001,"msg":"bad request","data":null}`
	err := decodeEnvelope(strings.NewReader(body), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T (%v)", err, err)
	}
	if apiErr.Code != 40001 || apiErr.Msg != "bad request" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestDecodeEnvelope_SurfacesVerificationFailedCode(t *testing.T) {
	err := decodeEnvelope(strings.NewReader(`{"code":630014,"msg":"Verification failed","data":null}`), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeVerificationFailed {
		t.Fatalf("err = %v, want *APIError with code %d", err, CodeVerificationFailed)
	}
}
