package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Client is a stateless AutoGLM API client.
type Client struct {
	http *http.Client
	base string
}

func NewClient() *Client { return NewClientWithBase(BaseURL) }

func NewClientWithBase(base string) *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second}, base: base}
}

// WithProxy returns a copy of the client whose HTTP requests are routed through
// proxyURL (http/https/socks5). An empty proxyURL returns the receiver unchanged
// (no proxy). A malformed proxyURL returns an error.
func (c *Client) WithProxy(proxyURL string) (*Client, error) {
	if proxyURL == "" {
		return c, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url %q: %w", proxyURL, err)
	}
	return &Client{
		http: &http.Client{
			Timeout:   c.http.Timeout,
			Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		},
		base: c.base,
	}, nil
}

// postSigned marshals body, sends a signed POST to path, and decodes data into out.
// bearer, when non-empty, is set as the authorization header.
func (c *Client) postSigned(ctx context.Context, path string, body any, bearer string, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header = signHeaders(time.Now().Unix())
	if bearer != "" {
		req.Header.Set("authorization", bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := decodeEnvelope(resp.Body, out); err != nil {
		return fmt.Errorf("http %d: %w", resp.StatusCode, err)
	}
	return nil
}

// getSigned sends a signed GET to path with the bearer token and decodes data into out.
func (c *Client) getSigned(ctx context.Context, path, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header = signHeaders(time.Now().Unix())
	if bearer != "" {
		req.Header.Set("authorization", bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := decodeEnvelope(resp.Body, out); err != nil {
		return fmt.Errorf("http %d: %w", resp.StatusCode, err)
	}
	return nil
}

type LoginResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	UserName     string `json:"user_name"`
	FirstLogin   bool   `json:"first_login"`
}

type Profile struct {
	Email    string `json:"email"`
	UserName string `json:"user_name"`
	UserID   string `json:"user_id"`
}

// Provider selects which OAuth broker AutoClaw authenticates through.
type Provider string

const (
	ProviderGoogle Provider = "google"
	ProviderZai    Provider = "zai"
)

// ParseProvider validates a provider string from an untrusted source. Empty
// defaults to Google. Unknown values error rather than becoming a URL path.
func ParseProvider(s string) (Provider, error) {
	switch Provider(s) {
	case "", ProviderGoogle:
		return ProviderGoogle, nil
	case ProviderZai:
		return ProviderZai, nil
	default:
		return "", fmt.Errorf("unknown login provider %q", s)
	}
}

// NavigateURIFor is the OAuth redirect (callback) URL for a provider.
func NavigateURIFor(p Provider) string {
	return "http://localhost:18432/auth/callback-" + string(p)
}

func (c *Client) OAuthURL(ctx context.Context, p Provider, deviceID string) (string, string, error) {
	body := map[string]string{"source_id": SourceID, "device_id": deviceID, "navigate_uri": NavigateURIFor(p)}
	var out struct {
		OAuthURL string `json:"oauth_url"`
		State    string `json:"state"`
	}
	if err := c.postSigned(ctx, "/userapi/overseasv1/"+string(p)+"-oauth-url", body, "", &out); err != nil {
		return "", "", err
	}
	return out.OAuthURL, out.State, nil
}

func (c *Client) OAuthLogin(ctx context.Context, p Provider, deviceID, code, state string) (LoginResult, error) {
	body := map[string]string{
		"source_id": SourceID, "device_id": deviceID,
		"code": code, "state": state, "navigate_uri": NavigateURIFor(p),
	}
	var out LoginResult
	err := c.postSigned(ctx, "/userapi/overseasv1/"+string(p)+"-oauth-login", body, "", &out)
	return out, err
}

func (c *Client) GoogleOAuthURL(ctx context.Context, deviceID string) (string, string, error) {
	return c.OAuthURL(ctx, ProviderGoogle, deviceID)
}

func (c *Client) GoogleOAuthLogin(ctx context.Context, deviceID, code, state string) (LoginResult, error) {
	return c.OAuthLogin(ctx, ProviderGoogle, deviceID, code, state)
}

func (c *Client) Refresh(ctx context.Context, deviceID, accessToken, refreshToken string) (string, string, error) {
	body := map[string]string{"source_id": SourceID, "device_id": deviceID, "refresh_token": refreshToken}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.postSigned(ctx, "/userapi/v1/refresh", body, accessToken, &out); err != nil {
		return "", "", err
	}
	newRefresh := out.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken // API may omit a new refresh token; keep the existing one
	}
	return out.AccessToken, newRefresh, nil
}

func (c *Client) UserProfile(ctx context.Context, deviceID, accessToken string) (Profile, error) {
	body := map[string]string{"source_id": SourceID, "device_id": deviceID}
	var out Profile
	err := c.postSigned(ctx, "/userapi/v1/user-profile", body, accessToken, &out)
	return out, err
}

// Wallet is the AutoClaw credit balance for an account: a grand total plus a
// per-type breakdown (reward / daily / subscription / fuel_pack / other).
type Wallet struct {
	TotalBalance int          `json:"total_balance"`
	Wallets      []WalletLine `json:"wallets"`
}

// WalletLine is one credit bucket within a Wallet.
type WalletLine struct {
	Type        string `json:"public_wallet_type"`
	DisplayName string `json:"display_name"`
	Balance     int    `json:"balance"`
	Display     bool   `json:"display"`
}

// Wallets fetches the credit balance for the bearer's account.
func (c *Client) Wallets(ctx context.Context, accessToken string) (Wallet, error) {
	var out Wallet
	err := c.getSigned(ctx, "/agent-assetmgr/api/v2/wallets?biz_app_id="+SourceID, accessToken, &out)
	return out, err
}
