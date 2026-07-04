# GogoClaw — Plan 1: Go Core (api, identity, store) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and unit-test GogoClaw's foundational Go packages — the AutoGLM API client (request signing + endpoints), device identity, JWT/envelope decoding, and the SQLite account store — verified against real captured data.

**Architecture:** Pure Go library packages under `internal/`, each with one responsibility and its own tests. No HTTP server or UI yet (those are Plan 2+). The API client is stateless; the store wraps `modernc.org/sqlite` (pure Go, no cgo). Everything here is exercised by unit tests using verified `(timestamp, signature)` pairs and a real JWT from the captured HAR, plus `httptest` mocks and a temp-file SQLite DB.

**Tech Stack:** Go 1.23+, stdlib (`crypto/md5`, `crypto/ed25519`, `crypto/sha256`, `crypto/rand`, `crypto/x509`, `encoding/pem`, `encoding/base64`, `encoding/json`, `net/http`, `database/sql`), `modernc.org/sqlite`.

## Global Constraints

- Go module name: `gogoclaw`. Go version floor: `1.23`.
- API base URL: `https://autoglm-api.autoglm.ai`.
- Signing constants (verbatim): `APP_ID = "100003"`, `APP_KEY = "38d2391985e2369a5fb8227d8e6cd5e5"`.
- `x-auth-sign = lowercasehex(MD5("<APP_ID>&<unix_seconds>&<APP_KEY>"))` — binds only appid+ts+key.
- Standard request headers: `x-version: 1.10.3`, `x-tm: win`, `x-product: autoclaw`, `x-channel: official`, `x-lang: en`, `content-type: application/json`, and the Electron `user-agent` string (see Task 1).
- Request-body constants: `source_id = "autoclaw"`, `navigate_uri = "http://localhost:18432/auth/callback-google"`.
- Response envelope: `{"code":0,"msg":"SUCCESS","data":{...}}`; non-zero `code` is an error. Responses are gzip on the wire — **never set `Accept-Encoding` manually** so `net/http` auto-decompresses.
- Tokens (`access_token`/`refresh_token`) are stored verbatim including the `"Bearer "` prefix.
- `device_id = lowercasehex(SHA256(raw32))` where `raw32` = SPKI-DER of an ed25519 public key minus its 12-byte prefix `302a300506032b6570032100`.
- Verified test vectors (from the captured HAR):
  - `sign(1783106439) = 6ad4a6d95693eb12f5b78c67e4c0e149`
  - `sign(1783106518) = e1d99088925c0f392c47ae60d835442b`
  - `sign(1783106519) = 127def37274d70f77f82753b340acdf6`
  - `sign(1783106751) = 30a44c28c72d1d32486e68f9f103d487`

## Plan roadmap (this is Plan 1 of 4)

1. **Plan 1 — Go core (this doc):** api client, identity, jwt/envelope, store. Deliverable: tested Go library.
2. **Plan 2 — Server + manual login:** AuthEngine, `ManualDriver`, callback listener `:18432`, REST + SSE, refresh scheduler, `go:embed`. Deliverable: working local server; add + refresh accounts manually.
3. **Plan 3 — Web dashboard:** Vite + React + TanStack Query/Table + Tailwind v4 + Phosphor. Deliverable: the UI.
4. **Plan 4 — Stealth auto-login:** Python CloakBrowser sidecar + `AutoDriver` + bulk UI. Deliverable: stealth bulk login.

Each subsequent plan is authored in full when Plan N-1 is complete.

---

### Task 1: Project scaffold + request signing (`internal/api/sign.go`)

**Files:**
- Create: `go.mod`
- Create: `internal/api/sign.go`
- Test: `internal/api/sign_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: package `api` with `func sign(ts int64) string`, `func signHeaders(ts int64) http.Header`, and the exported constants block (`BaseURL`, `SourceID`, `NavigateURI`). `sign`/`signHeaders` are unexported (used by Task 4).

- [ ] **Step 1: Create the Go module**

Run:
```bash
cd /e/autoclaw && go mod init gogoclaw && go mod edit -go=1.23
```
Expected: creates `go.mod` with `module gogoclaw` and `go 1.23`.

- [ ] **Step 2: Write the failing test**

Create `internal/api/sign_test.go`:
```go
package api

import (
	"regexp"
	"testing"
)

func TestSign_KnownVectors(t *testing.T) {
	// (timestamp, signature) pairs captured live from the AutoClaw client.
	cases := map[int64]string{
		1783106439: "6ad4a6d95693eb12f5b78c67e4c0e149",
		1783106518: "e1d99088925c0f392c47ae60d835442b",
		1783106519: "127def37274d70f77f82753b340acdf6",
		1783106751: "30a44c28c72d1d32486e68f9f103d487",
	}
	for ts, want := range cases {
		if got := sign(ts); got != want {
			t.Errorf("sign(%d) = %s, want %s", ts, got, want)
		}
	}
}

func TestSignHeaders(t *testing.T) {
	h := signHeaders(1783106518)
	if h.Get("x-auth-appid") != "100003" {
		t.Errorf("appid = %q", h.Get("x-auth-appid"))
	}
	if h.Get("x-auth-timestamp") != "1783106518" {
		t.Errorf("timestamp = %q", h.Get("x-auth-timestamp"))
	}
	if h.Get("x-auth-sign") != "e1d99088925c0f392c47ae60d835442b" {
		t.Errorf("sign = %q", h.Get("x-auth-sign"))
	}
	if h.Get("x-product") != "autoclaw" || h.Get("x-version") != "1.10.3" {
		t.Errorf("static headers wrong: %v", h)
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidRe.MatchString(h.Get("x-trace-id")) {
		t.Errorf("x-trace-id not a uuid v4: %q", h.Get("x-trace-id"))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/api/ -run 'TestSign' -v`
Expected: FAIL — `undefined: sign` / `undefined: signHeaders`.

- [ ] **Step 4: Write minimal implementation**

Create `internal/api/sign.go`:
```go
package api

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
)

const (
	BaseURL     = "https://autoglm-api.autoglm.ai"
	SourceID    = "autoclaw"
	NavigateURI = "http://localhost:18432/auth/callback-google"

	appID   = "100003"
	appKey  = "38d2391985e2369a5fb8227d8e6cd5e5"
	version = "1.10.3"
	product = "autoclaw"
	tm      = "win"
	channel = "official"
	lang    = "en"

	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) autoclaw/1.10.3 Chrome/130.0.6723.191 Electron/33.4.11 Safari/537.36"
)

// sign returns the x-auth-sign value for a unix-seconds timestamp.
func sign(ts int64) string {
	sum := md5.Sum([]byte(fmt.Sprintf("%s&%d&%s", appID, ts, appKey)))
	return hex.EncodeToString(sum[:])
}

// randUUID returns a random RFC-4122 v4 UUID.
func randUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// signHeaders builds the full set of signed request headers for a timestamp.
func signHeaders(ts int64) http.Header {
	h := http.Header{}
	h.Set("content-type", "application/json")
	h.Set("x-auth-appid", appID)
	h.Set("x-auth-timestamp", strconv.FormatInt(ts, 10))
	h.Set("x-auth-sign", sign(ts))
	h.Set("x-trace-id", randUUID())
	h.Set("x-version", version)
	h.Set("x-tm", tm)
	h.Set("x-product", product)
	h.Set("x-channel", channel)
	h.Set("x-lang", lang)
	h.Set("user-agent", userAgent)
	return h
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/api/ -run 'TestSign' -v`
Expected: PASS (both tests).

- [ ] **Step 6: Commit**

```bash
git add go.mod internal/api/sign.go internal/api/sign_test.go
git commit -m "feat(api): request signing with verified HAR vectors"
```

---

### Task 2: Device identity (`internal/identity/identity.go`)

**Files:**
- Create: `internal/identity/identity.go`
- Test: `internal/identity/identity_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `identity.Identity{DeviceID, PubPEM, PrivPEM string}`, `func New() (Identity, error)`, `func DeviceIDFromPub(pub ed25519.PublicKey) (string, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/identity/identity_test.go`:
```go
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNew_DeviceIDShape(t *testing.T) {
	id, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(id.DeviceID) != 64 {
		t.Errorf("device_id length = %d, want 64", len(id.DeviceID))
	}
	if _, err := hex.DecodeString(id.DeviceID); err != nil {
		t.Errorf("device_id not hex: %v", err)
	}
	if !strings.Contains(id.PrivPEM, "PRIVATE KEY") || !strings.Contains(id.PubPEM, "PUBLIC KEY") {
		t.Errorf("PEMs malformed")
	}
}

func TestNew_Unique(t *testing.T) {
	a, _ := New()
	b, _ := New()
	if a.DeviceID == b.DeviceID {
		t.Errorf("two identities share a device_id")
	}
}

func TestDeviceIDFromPub_Deterministic(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	first, err := DeviceIDFromPub(pub)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := DeviceIDFromPub(pub)
	if first != second {
		t.Errorf("non-deterministic: %s != %s", first, second)
	}
	if len(first) != 64 {
		t.Errorf("length = %d", len(first))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/identity/ -v`
Expected: FAIL — `undefined: New` / `undefined: DeviceIDFromPub`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/identity/identity.go`:
```go
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
)

// Identity is a per-account device credential.
type Identity struct {
	DeviceID string
	PubPEM   string
	PrivPEM  string
}

// DeviceIDFromPub derives the AutoClaw device_id: SHA256 of the raw 32-byte
// ed25519 public key (SPKI-DER minus its 12-byte prefix), hex-encoded.
func DeviceIDFromPub(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	if len(der) != 44 {
		return "", fmt.Errorf("unexpected SPKI length %d", len(der))
	}
	sum := sha256.Sum256(der[12:]) // strip 12-byte ed25519 SPKI prefix
	return hex.EncodeToString(sum[:]), nil
}

// New generates a fresh ed25519 keypair and derives its identity.
func New() (Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	deviceID, err := DeviceIDFromPub(pub)
	if err != nil {
		return Identity{}, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return Identity{}, err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return Identity{}, err
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	return Identity{DeviceID: deviceID, PubPEM: string(pubPEM), PrivPEM: string(privPEM)}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/identity/ -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/identity/
git commit -m "feat(identity): ed25519 device_id derivation"
```

---

### Task 3: JWT claims + response envelope (`internal/api/jwt.go`, `internal/api/envelope.go`)

**Files:**
- Create: `internal/api/jwt.go`
- Create: `internal/api/envelope.go`
- Test: `internal/api/jwt_test.go`
- Test: `internal/api/envelope_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `api.Claims{Email string; Exp int64; DeviceID string}`, `func ParseClaims(bearer string) (Claims, error)`; and unexported `func decodeEnvelope(r io.Reader, out any) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/api/jwt_test.go` (the token is the real access_token from the captured `google-oauth-login` response):
```go
package api

import "testing"

const sampleAccessToken = "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
	"eyJ1c2VyX2lkIjo5NTc5NiwiZGV2aWNlX2lkIjoiZWQ3NmVkMTAzZWFlNjk1ZjU1NDllMmZmNzhjYTNjZmViNjdlMmQ5ZWRlNjkwYzdjNmM1MGNlMWZkNjQxNjU2NSIsInNvdXJjZV9pZCI6ImF1dG9jbGF3YWNjZXNzX3Rva2VuIiwiZ3VpZCI6IiIsImlzX2d1ZXN0IjpmYWxzZSwicG93ZXIiOjAsImV4cCI6MTc4MzE5MjkxOCwiaWF0IjoxNzgzMTA2NTE4LCJqdGkiOiJldm1zbmlwZUBnbWFpbC5jb20ifQ." +
	"7kHC2ivutZ6WteqMB2bwpuwFMwEzNvZ4BOSUneXxKIg"

func TestParseClaims(t *testing.T) {
	c, err := ParseClaims(sampleAccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "evmsnipe@gmail.com" {
		t.Errorf("Email = %q", c.Email)
	}
	if c.Exp != 1783192918 {
		t.Errorf("Exp = %d", c.Exp)
	}
	if c.DeviceID != "ed76ed103eae695f5549e2ff78ca3cfeb67e2d9ede690c7c6c50ce1fd6416565" {
		t.Errorf("DeviceID = %q", c.DeviceID)
	}
}

func TestParseClaims_Malformed(t *testing.T) {
	if _, err := ParseClaims("Bearer not.a.jwt.at.all"); err == nil {
		t.Error("expected error for malformed token")
	}
}
```

Create `internal/api/envelope_test.go`:
```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ -run 'TestParseClaims|TestDecodeEnvelope' -v`
Expected: FAIL — `undefined: ParseClaims` / `undefined: decodeEnvelope`.

- [ ] **Step 3: Write minimal implementations**

Create `internal/api/jwt.go`:
```go
package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Claims holds the fields we read from an AutoClaw access-token JWT payload.
type Claims struct {
	Email    string // jti
	Exp      int64
	DeviceID string
}

// ParseClaims decodes (without verifying) the payload of a "Bearer <jwt>" token.
func ParseClaims(bearer string) (Claims, error) {
	tok := strings.TrimPrefix(bearer, "Bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("not a JWT: %d segments", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("decode payload: %w", err)
	}
	var raw struct {
		Jti      string `json:"jti"`
		Exp      int64  `json:"exp"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, fmt.Errorf("unmarshal payload: %w", err)
	}
	return Claims{Email: raw.Jti, Exp: raw.Exp, DeviceID: raw.DeviceID}, nil
}
```

Create `internal/api/envelope.go`:
```go
package api

import (
	"encoding/json"
	"fmt"
	"io"
)

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// decodeEnvelope reads the {code,msg,data} wrapper; a non-zero code is an error.
// When out is non-nil, data is unmarshaled into it.
func decodeEnvelope(r io.Reader, out any) error {
	var e envelope
	if err := json.NewDecoder(r).Decode(&e); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	if e.Code != 0 {
		return fmt.Errorf("api error %d: %s", e.Code, e.Msg)
	}
	if out != nil {
		return json.Unmarshal(e.Data, out)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -run 'TestParseClaims|TestDecodeEnvelope' -v`
Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add internal/api/jwt.go internal/api/jwt_test.go internal/api/envelope.go internal/api/envelope_test.go
git commit -m "feat(api): JWT claim + response envelope decoding"
```

---

### Task 4: API client endpoints (`internal/api/client.go`)

**Files:**
- Create: `internal/api/client.go`
- Test: `internal/api/client_test.go`

**Interfaces:**
- Consumes: `sign.go` (`signHeaders`, `BaseURL`, `SourceID`, `NavigateURI`), `envelope.go` (`decodeEnvelope`).
- Produces:
  - `api.LoginResult{AccessToken, RefreshToken, UserID, UserName string; FirstLogin bool}`
  - `api.Profile{Email, UserName, UserID string}`
  - `func NewClient() *Client`, `func NewClientWithBase(base string) *Client`
  - `func (c *Client) GoogleOAuthURL(ctx context.Context, deviceID string) (oauthURL, state string, err error)`
  - `func (c *Client) GoogleOAuthLogin(ctx context.Context, deviceID, code, state string) (LoginResult, error)`
  - `func (c *Client) Refresh(ctx context.Context, deviceID, accessToken, refreshToken string) (access, refresh string, err error)`
  - `func (c *Client) UserProfile(ctx context.Context, deviceID, accessToken string) (Profile, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/api/client_test.go`:
```go
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient spins up a mock AutoGLM server; handler receives (path, body).
func newTestClient(t *testing.T, handler func(path string, body map[string]any) any) (*Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-auth-sign") == "" || r.Header.Get("x-auth-appid") != "100003" {
			t.Errorf("missing signing headers on %s", r.URL.Path)
		}
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		data := handler(r.URL.Path, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	}))
	return NewClientWithBase(srv.URL), srv.Close
}

func TestGoogleOAuthURL(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any) any {
		if path != "/userapi/overseasv1/google-oauth-url" {
			t.Errorf("path = %s", path)
		}
		if body["source_id"] != "autoclaw" || body["device_id"] != "dev123" {
			t.Errorf("body = %v", body)
		}
		if body["navigate_uri"] != NavigateURI {
			t.Errorf("navigate_uri = %v", body["navigate_uri"])
		}
		return map[string]any{"oauth_url": "https://accounts.google.com/x", "state": "st1"}
	})
	defer done()
	url, state, err := c.GoogleOAuthURL(context.Background(), "dev123")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://accounts.google.com/x" || state != "st1" {
		t.Errorf("got %s / %s", url, state)
	}
}

func TestGoogleOAuthLogin(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any) any {
		if path != "/userapi/overseasv1/google-oauth-login" {
			t.Errorf("path = %s", path)
		}
		if body["code"] != "auth-code" || body["state"] != "st1" {
			t.Errorf("body = %v", body)
		}
		return map[string]any{
			"access_token": "Bearer aaa", "refresh_token": "Bearer rrr",
			"user_id": "hexuser", "user_name": "EVM Snipe", "first_login": true,
		}
	})
	defer done()
	res, err := c.GoogleOAuthLogin(context.Background(), "dev123", "auth-code", "st1")
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken != "Bearer aaa" || res.RefreshToken != "Bearer rrr" || !res.FirstLogin {
		t.Errorf("res = %+v", res)
	}
}

func TestRefresh(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any) any {
		if path != "/userapi/v1/refresh" {
			t.Errorf("path = %s", path)
		}
		if body["refresh_token"] != "Bearer rrr" {
			t.Errorf("body = %v", body)
		}
		return map[string]any{"access_token": "Bearer new-a", "refresh_token": "Bearer new-r", "refresh": false}
	})
	defer done()
	access, refresh, err := c.Refresh(context.Background(), "dev123", "Bearer old-a", "Bearer rrr")
	if err != nil {
		t.Fatal(err)
	}
	if access != "Bearer new-a" || refresh != "Bearer new-r" {
		t.Errorf("got %s / %s", access, refresh)
	}
}

func TestUserProfile(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any) any {
		if path != "/userapi/v1/user-profile" {
			t.Errorf("path = %s", path)
		}
		return map[string]any{"email": "evmsnipe@gmail.com", "user_name": "EVM Snipe", "user_id": "hexuser"}
	})
	defer done()
	p, err := c.UserProfile(context.Background(), "dev123", "Bearer aaa")
	if err != nil {
		t.Fatal(err)
	}
	if p.Email != "evmsnipe@gmail.com" {
		t.Errorf("profile = %+v", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run 'TestGoogleOAuth|TestRefresh|TestUserProfile' -v`
Expected: FAIL — `undefined: NewClientWithBase` etc.

- [ ] **Step 3: Write minimal implementation**

Create `internal/api/client.go`:
```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
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
	return decodeEnvelope(resp.Body, out)
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
	return out.AccessToken, out.RefreshToken, nil
}

func (c *Client) UserProfile(ctx context.Context, deviceID, accessToken string) (Profile, error) {
	body := map[string]string{"source_id": SourceID, "device_id": deviceID}
	var out Profile
	err := c.postSigned(ctx, "/userapi/v1/user-profile", body, accessToken, &out)
	return out, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -v`
Expected: PASS (all api tests).

- [ ] **Step 5: Commit**

```bash
git add internal/api/client.go internal/api/client_test.go
git commit -m "feat(api): OAuth URL/login, refresh, and profile endpoints"
```

---

### Task 5: Account store (`internal/store/store.go`, `internal/store/sqlite.go`)

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/sqlite.go`
- Test: `internal/store/sqlite_test.go`

**Interfaces:**
- Consumes: nothing (holds token strings + expiry produced by Plan 2's AuthEngine).
- Produces:
  - `store.Account` struct (fields below).
  - `store.Store` interface: `Add(Account) error`, `List() ([]Account, error)`, `Get(email string) (Account, error)`, `UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error`, `SetStatus(email, status string) error`, `Delete(email string) error`.
  - `store.Status*` constants: `StatusActive = "active"`, `StatusRefreshFailed = "refresh_failed"`, `StatusNeedsRelogin = "needs_relogin"`.
  - `func Open(path string) (*SQLiteStore, error)` (implements `Store`).

- [ ] **Step 1: Add the SQLite dependency**

Run:
```bash
cd /e/autoclaw && go get modernc.org/sqlite
```
Expected: adds `modernc.org/sqlite` to `go.mod`/`go.sum`.

- [ ] **Step 2: Write the failing test**

Create `internal/store/sqlite_test.go`:
```go
package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleAccount() Account {
	now := time.Now().Truncate(time.Second)
	return Account{
		Email: "a@example.com", UserID: "u1", DeviceID: "dev1",
		AccessToken: "Bearer a", RefreshToken: "Bearer r",
		AccessExpiresAt: now.Add(24 * time.Hour), RefreshExpiresAt: now.Add(720 * time.Hour),
		PrivPEM: "PRIV", PubPEM: "PUB", AddedAt: now, LastRefreshedAt: now,
		Status: StatusActive,
	}
}

func TestAddAndGet(t *testing.T) {
	s := newTestStore(t)
	want := sampleAccount()
	if err := s.Add(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != want.Email || got.AccessToken != want.AccessToken || got.Status != StatusActive {
		t.Errorf("got = %+v", got)
	}
	if !got.AccessExpiresAt.Equal(want.AccessExpiresAt) {
		t.Errorf("aexp = %v want %v", got.AccessExpiresAt, want.AccessExpiresAt)
	}
}

func TestAdd_ReplacesDuplicate(t *testing.T) {
	s := newTestStore(t)
	a := sampleAccount()
	_ = s.Add(a)
	a.AccessToken = "Bearer a2"
	if err := s.Add(a); err != nil {
		t.Fatalf("re-add should upsert, got %v", err)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Errorf("len = %d, want 1", len(list))
	}
	if list[0].AccessToken != "Bearer a2" {
		t.Errorf("token not updated: %s", list[0].AccessToken)
	}
}

func TestUpdateTokensAndStatus(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(sampleAccount())
	newAexp := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	if err := s.UpdateTokens("a@example.com", "Bearer na", "Bearer nr", newAexp, newAexp); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus("a@example.com", StatusNeedsRelogin); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("a@example.com")
	if got.AccessToken != "Bearer na" || got.Status != StatusNeedsRelogin {
		t.Errorf("got = %+v", got)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(sampleAccount())
	if err := s.Delete("a@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a@example.com"); err == nil {
		t.Error("expected error getting deleted account")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/store/ -v`
Expected: FAIL — `undefined: Open` / `undefined: Account`.

- [ ] **Step 4: Write minimal implementation**

Create `internal/store/store.go`:
```go
package store

import "time"

const (
	StatusActive        = "active"
	StatusRefreshFailed = "refresh_failed"
	StatusNeedsRelogin  = "needs_relogin"
)

// Account is a stored AutoClaw account with its live tokens and device identity.
type Account struct {
	Email            string
	UserID           string
	DeviceID         string
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	PrivPEM          string
	PubPEM           string
	AddedAt          time.Time
	LastRefreshedAt  time.Time
	Status           string
}

// Store persists accounts.
type Store interface {
	Add(Account) error
	List() ([]Account, error)
	Get(email string) (Account, error)
	UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error
	SetStatus(email, status string) error
	Delete(email string) error
}
```

Create `internal/store/sqlite.go`:
```go
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
  email             TEXT PRIMARY KEY,
  user_id           TEXT NOT NULL,
  device_id         TEXT NOT NULL,
  access_token      TEXT NOT NULL,
  refresh_token     TEXT NOT NULL,
  access_expires_at INTEGER NOT NULL,
  refresh_expires_at INTEGER NOT NULL,
  priv_pem          TEXT NOT NULL,
  pub_pem           TEXT NOT NULL,
  added_at          INTEGER NOT NULL,
  last_refreshed_at INTEGER NOT NULL,
  status            TEXT NOT NULL
);`

// SQLiteStore is a pure-Go SQLite-backed Store.
type SQLiteStore struct{ db *sql.DB }

func Open(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) Add(a Account) error {
	_, err := s.db.Exec(`
INSERT INTO accounts (email,user_id,device_id,access_token,refresh_token,
  access_expires_at,refresh_expires_at,priv_pem,pub_pem,added_at,last_refreshed_at,status)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(email) DO UPDATE SET
  user_id=excluded.user_id, device_id=excluded.device_id,
  access_token=excluded.access_token, refresh_token=excluded.refresh_token,
  access_expires_at=excluded.access_expires_at, refresh_expires_at=excluded.refresh_expires_at,
  priv_pem=excluded.priv_pem, pub_pem=excluded.pub_pem,
  last_refreshed_at=excluded.last_refreshed_at, status=excluded.status`,
		a.Email, a.UserID, a.DeviceID, a.AccessToken, a.RefreshToken,
		a.AccessExpiresAt.Unix(), a.RefreshExpiresAt.Unix(), a.PrivPEM, a.PubPEM,
		a.AddedAt.Unix(), a.LastRefreshedAt.Unix(), a.Status)
	return err
}

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var aexp, rexp, added, refreshed int64
	err := sc.Scan(&a.Email, &a.UserID, &a.DeviceID, &a.AccessToken, &a.RefreshToken,
		&aexp, &rexp, &a.PrivPEM, &a.PubPEM, &added, &refreshed, &a.Status)
	if err != nil {
		return Account{}, err
	}
	a.AccessExpiresAt = time.Unix(aexp, 0)
	a.RefreshExpiresAt = time.Unix(rexp, 0)
	a.AddedAt = time.Unix(added, 0)
	a.LastRefreshedAt = time.Unix(refreshed, 0)
	return a, nil
}

const selectCols = `email,user_id,device_id,access_token,refresh_token,
  access_expires_at,refresh_expires_at,priv_pem,pub_pem,added_at,last_refreshed_at,status`

func (s *SQLiteStore) Get(email string) (Account, error) {
	row := s.db.QueryRow(`SELECT `+selectCols+` FROM accounts WHERE email=?`, email)
	a, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return Account{}, fmt.Errorf("account %q not found", email)
	}
	return a, err
}

func (s *SQLiteStore) List() ([]Account, error) {
	rows, err := s.db.Query(`SELECT ` + selectCols + ` FROM accounts ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error {
	res, err := s.db.Exec(`UPDATE accounts SET access_token=?, refresh_token=?,
  access_expires_at=?, refresh_expires_at=?, last_refreshed_at=?, status=? WHERE email=?`,
		access, refresh, aexp.Unix(), rexp.Unix(), time.Now().Unix(), StatusActive, email)
	if err != nil {
		return err
	}
	return mustAffect(res, email)
}

func (s *SQLiteStore) SetStatus(email, status string) error {
	res, err := s.db.Exec(`UPDATE accounts SET status=? WHERE email=?`, status, email)
	if err != nil {
		return err
	}
	return mustAffect(res, email)
}

func (s *SQLiteStore) Delete(email string) error {
	_, err := s.db.Exec(`DELETE FROM accounts WHERE email=?`, email)
	return err
}

func mustAffect(res sql.Result, email string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("account %q not found", email)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS (all store tests).

- [ ] **Step 6: Run the full suite + tidy**

Run:
```bash
cd /e/autoclaw && go mod tidy && go test ./... -v
```
Expected: all packages PASS.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/store/
git commit -m "feat(store): SQLite account store (modernc, pure Go)"
```

---

## Notes for the implementer

- **`UpdateTokens` sets status to `active`** by design — a successful refresh clears a prior failure. Plan 2's refresher relies on this.
- **`UserID` differs by source:** the `google-oauth-login` / `user-profile` responses return a hex `user_id` string; the JWT payload carries a numeric `user_id`. The store keeps the response's string form. Email always comes from the JWT `jti`.
- **Token strings keep their `"Bearer "` prefix** everywhere (store, refresh body, authorization header build in Plan 2).
- **Do not add `Accept-Encoding`** to any request — gzip is handled transparently by `net/http`.
