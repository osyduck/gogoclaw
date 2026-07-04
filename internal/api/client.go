package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

func (c *Client) GoogleOAuthURL(ctx context.Context, deviceID string) (string, string, error) {
	body := map[string]string{"source_id": SourceID, "device_id": deviceID, "navigate_uri": NavigateURI}
	var out struct {
		OAuthURL string `json:"oauth_url"`
		State    string `json:"state"`
	}
	if err := c.postSigned(ctx, "/userapi/overseasv1/google-oauth-url", body, "", &out); err != nil {
		return "", "", err
	}
	return out.OAuthURL, out.State, nil
}

func (c *Client) GoogleOAuthLogin(ctx context.Context, deviceID, code, state string) (LoginResult, error) {
	body := map[string]string{
		"source_id": SourceID, "device_id": deviceID,
		"code": code, "state": state, "navigate_uri": NavigateURI,
	}
	var out LoginResult
	err := c.postSigned(ctx, "/userapi/overseasv1/google-oauth-login", body, "", &out)
	return out, err
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
