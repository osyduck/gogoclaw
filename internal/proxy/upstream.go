package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"

	"gogoclaw/internal/api"
	"gogoclaw/internal/store"
)

const (
	internalBearer = "Bearer autoclaw-internal-proxy"
	chatPath       = "/autoclaw-proxy/proxy/autoclaw/chat/completions"
	maxAttempts    = 5
)

// Picker supplies accounts for forwarding, with failover advancement.
type Picker interface {
	Pick() (store.Account, error)
	Next(tried map[string]bool) (store.Account, error)
}

// Forwarder issues streaming POSTs to the AutoClaw chat-completions upstream.
type Forwarder struct {
	http *http.Client
	base string
}

// NewForwarder builds a Forwarder against a base URL (api.BaseURL in prod).
// No client-level timeout: streaming lifetime is governed by the request context.
func NewForwarder(base string) *Forwarder {
	return &Forwarder{http: &http.Client{}, base: base}
}

// uuid returns a random RFC-4122 v4 UUID.
func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Do performs a single upstream attempt for one account. The caller owns
// resp.Body and must close it.
func (f *Forwarder) Do(ctx context.Context, acct store.Account, prefixedModel string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.base+chatPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	sid := uuid()
	sessionKey := "agent:main:" + sid[:8]
	h := req.Header
	h.Set("content-type", "application/json")
	h.Set("accept", "application/json")
	h.Set("authorization", internalBearer)
	h.Set("X-Authorization", "Bearer "+acct.AccessToken)
	h.Set("X-Request-Model", prefixedModel)
	h.Set("x_trace_id", "autoclaw-desktop")
	h.Set("X-Version", api.Version)
	h.Set("X-Product", api.Product)
	h.Set("X-Tm", api.TM)
	h.Set("X-Channel", api.Channel)
	h.Set("X-Lang", api.Lang)
	h.Set("X-Client-Type", "pc")
	h.Set("X-Autoclaw-Source", "desktop")
	h.Set("X-Agent-Id", "main")
	h.Set("X-Autoclaw-Agent-Id", "main")
	h.Set("X-Session-Id", uuid())
	h.Set("X-Session-Key", sessionKey)
	h.Set("X-Autoclaw-Session-Key", sessionKey)
	h.Set("X-Request-Id", uuid())
	h.Set("User-Agent", api.UserAgent)
	return f.http.Do(req)
}

// isRetryable reports whether an upstream status warrants trying another account.
func isRetryable(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

// Forward picks an account and forwards, failing over to other accounts on
// retryable errors (max maxAttempts). It returns the first response that is
// either 2xx or a non-retryable status (surfaced as-is). The caller owns
// resp.Body. On total failure it returns the last error (or last response).
func (f *Forwarder) Forward(ctx context.Context, p Picker, prefixedModel string, body []byte) (*http.Response, store.Account, error) {
	acct, err := p.Pick()
	if err != nil {
		return nil, store.Account{}, err
	}
	tried := map[string]bool{}
	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, doErr := f.Do(ctx, acct, prefixedModel, body)
		if doErr == nil && !isRetryable(resp.StatusCode) {
			return resp, acct, nil // 2xx or non-retryable: surface
		}
		// Record failure and advance.
		if doErr != nil {
			lastErr, lastResp = doErr, nil
		} else {
			lastErr, lastResp = nil, resp
			resp.Body.Close()
		}
		tried[acct.Email] = true
		next, nerr := p.Next(tried)
		if nerr != nil {
			break // exhausted eligible accounts
		}
		acct = next
	}
	if lastResp != nil {
		// Re-issue the final failing account once so the caller can read the
		// real upstream error body (the earlier one was closed during the loop).
		resp, doErr := f.Do(ctx, acct, prefixedModel, body)
		if doErr == nil {
			return resp, acct, nil
		}
		lastErr = doErr
	}
	if lastErr == nil {
		lastErr = errors.New("upstream forwarding failed")
	}
	return nil, acct, lastErr
}
