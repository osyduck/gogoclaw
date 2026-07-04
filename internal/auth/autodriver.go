package auth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gogoclaw/internal/api"
)

// AutoDriver drives Google consent via the CloakBrowser sidecar (Plan 4).
type AutoDriver struct {
	sidecarURL string
	http       *http.Client
	sem        chan struct{}
}

func NewAutoDriver(sidecarURL string) *AutoDriver {
	return &AutoDriver{sidecarURL: sidecarURL, http: &http.Client{Timeout: 3 * time.Minute}, sem: make(chan struct{}, 3)}
}

// Drive posts the credentials to the sidecar's /drive and blocks until it reports
// reaching the callback redirect (ok) or failing (reason).
func (d *AutoDriver) Drive(ctx context.Context, provider api.Provider, oauthURL string, cred *GoogleCred, onStep func(string)) error {
	if cred == nil {
		return fmt.Errorf("auto login requires Google credentials")
	}
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	payload := map[string]string{
		"oauth_url": oauthURL, "email": cred.Email, "password": cred.Password,
		"proxy": cred.Proxy, "provider": string(provider),
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sidecar %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// The sidecar streams newline-delimited JSON: {"step":...} lines as each
	// action happens, then a final {"done":true,"ok":...,"reason":...} line.
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var done bool
	var out struct {
		Done   bool   `json:"done"`
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
		Step   string `json:"step"`
	}
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		out = struct {
			Done   bool   `json:"done"`
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
			Step   string `json:"step"`
		}{}
		if err := json.Unmarshal(line, &out); err != nil {
			continue // ignore malformed lines
		}
		if out.Done {
			done = true
			if !out.OK {
				return fmt.Errorf("stealth login failed: %s", out.Reason)
			}
			return nil
		}
		if out.Step != "" && onStep != nil {
			onStep(out.Step)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read sidecar stream: %w", err)
	}
	if !done {
		return fmt.Errorf("sidecar closed stream without a result")
	}
	return nil
}
