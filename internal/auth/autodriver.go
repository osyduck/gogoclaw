package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// AutoDriver drives Google consent via the CloakBrowser sidecar (Plan 4).
type AutoDriver struct {
	sidecarURL string
	http       *http.Client
}

func NewAutoDriver(sidecarURL string) *AutoDriver {
	return &AutoDriver{sidecarURL: sidecarURL, http: &http.Client{Timeout: 3 * time.Minute}}
}

// Drive posts the credentials to the sidecar's /drive and blocks until it reports
// reaching the callback redirect (ok) or failing (reason).
func (d *AutoDriver) Drive(ctx context.Context, oauthURL string, cred *GoogleCred) error {
	if cred == nil {
		return fmt.Errorf("auto login requires Google credentials")
	}
	payload := map[string]string{
		"oauth_url": oauthURL, "email": cred.Email, "password": cred.Password, "proxy": cred.Proxy,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.sidecarURL+"/drive", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode sidecar response: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("stealth login failed: %s", out.Reason)
	}
	return nil
}
