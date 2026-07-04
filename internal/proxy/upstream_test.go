package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogoclaw/internal/store"
)

// fakePicker drives Forward with a scripted account sequence.
type fakePicker struct {
	accts []store.Account
	i     int
}

func (f *fakePicker) Pick() (store.Account, error) {
	if f.i >= len(f.accts) {
		return store.Account{}, ErrNoEligible
	}
	return f.accts[f.i], nil
}

func (f *fakePicker) Next(tried map[string]bool) (store.Account, error) {
	f.i++
	if f.i >= len(f.accts) {
		return store.Account{}, ErrNoEligible
	}
	return f.accts[f.i], nil
}

func TestDoSetsUpstreamHeaders(t *testing.T) {
	var gotInternal, gotXAuth, gotModel, gotProduct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInternal = r.Header.Get("authorization")
		gotXAuth = r.Header.Get("X-Authorization")
		gotModel = r.Header.Get("X-Request-Model")
		gotProduct = r.Header.Get("X-Product")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	f := NewForwarder(srv.URL)
	resp, err := f.Do(context.Background(),
		store.Account{Email: "a@x.com", AccessToken: "JWT123"},
		"openrouter_glm-5.2", []byte(`{"model":"glm-5.2"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if gotInternal != "Bearer autoclaw-internal-proxy" {
		t.Errorf("internal auth = %q", gotInternal)
	}
	if gotXAuth != "Bearer JWT123" {
		t.Errorf("x-authorization = %q", gotXAuth)
	}
	if gotModel != "openrouter_glm-5.2" {
		t.Errorf("x-request-model = %q", gotModel)
	}
	if gotProduct != "autoclaw" {
		t.Errorf("x-product = %q", gotProduct)
	}
}

func TestForwardFailsOverOn429(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt := strings.TrimPrefix(r.Header.Get("X-Authorization"), "Bearer ")
		seen = append(seen, jwt)
		if jwt == "good" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: ok\n\n")
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := &fakePicker{accts: []store.Account{
		{Email: "bad@x.com", AccessToken: "bad"},
		{Email: "good@x.com", AccessToken: "good"},
	}}
	f := NewForwarder(srv.URL)
	resp, used, err := f.Forward(context.Background(), p, "zai_auto", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || used.Email != "good@x.com" {
		t.Fatalf("used=%s status=%d", used.Email, resp.StatusCode)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %v", seen)
	}
}

func TestForwardSurfacesNonRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	p := &fakePicker{accts: []store.Account{{Email: "a@x.com", AccessToken: "t"}}}
	f := NewForwarder(srv.URL)
	resp, _, err := f.Forward(context.Background(), p, "zai_auto", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 surfaced as-is", resp.StatusCode)
	}
}
