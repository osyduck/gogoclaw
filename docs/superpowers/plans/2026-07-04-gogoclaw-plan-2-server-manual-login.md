# GogoClaw — Plan 2: Server + Manual Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the Plan 1 core library into a running local server on `127.0.0.1:18432` that logs accounts in via Google OAuth (manual flow, original loopback callback), auto-refreshes their tokens, and exposes a REST + SSE API a dashboard can consume.

**Architecture:** A single `http.Server` on `127.0.0.1:18432` wires together: an `AuthEngine` (pending-login state machine + pluggable `LoginDriver`; `ManualDriver` here), a `Refresher` (background token refresh with failure classification), an in-process `events.Bus` (pub/sub → SSE), and the existing `store`/`api`/`identity` packages. The Google browser redirect hits `/auth/callback-google`, which the engine exchanges for tokens — the same callback path both manual (this plan) and stealth-auto (Plan 4) drivers converge on.

**Tech Stack:** Go 1.25+, stdlib `net/http` (Go 1.22+ method+wildcard `ServeMux`, SSE via `http.Flusher`), `embed`, plus Plan 1's packages (`internal/api`, `internal/identity`, `internal/store`).

## Global Constraints

- Go module `gogoclaw`; Go floor `1.25.0`. Bind loopback only: `127.0.0.1:18432` (never `0.0.0.0`).
- Callback path is exactly `/auth/callback-google` (matches `api.NavigateURI = "http://localhost:18432/auth/callback-google"`).
- `source_id = "autoclaw"`; every account gets its own `identity.New()` (ed25519 device_id) generated at login start and persisted.
- Tokens stored verbatim incl. `"Bearer "`. Email comes from the access-token JWT `jti`; access/refresh expiry from each token's JWT `exp`.
- Access token TTL ~24h, refresh ~30d. Refresh margin: refresh when `now >= AccessExpiresAt - 5m`. Scheduler tick: 1 minute.
- Google credentials (auto flow, Plan 4) are never persisted; this plan's manual flow needs none.
- No `accounts.db` encryption. The REST API must not expose raw bearer tokens in list responses.
- Reuse Plan 1 interfaces verbatim: `api.Client` (`GoogleOAuthURL`, `GoogleOAuthLogin`, `Refresh`, `UserProfile`, `NewClient`, `NewClientWithBase`), `api.ParseClaims`→`Claims{Email,Exp,DeviceID}`, `identity.New()`→`Identity{DeviceID,PubPEM,PrivPEM}`, `store.Store` (`Add`,`List`,`Get`,`UpdateTokens`,`SetStatus`,`Delete`) + `store.Open`, `store.Account`, `store.Status*`.

## Plan roadmap (this is Plan 2 of 4)

1. Plan 1 — Go core ✅ (api, identity, store).
2. **Plan 2 — Server + manual login (this doc):** classifiable errors, events bus, AuthEngine + ManualDriver, Refresher, HTTP server + callback + SSE + embed, main wiring. Deliverable: run the binary, add accounts by clicking a login link, tokens auto-refresh.
3. Plan 3 — Web dashboard (React/TanStack/Tailwind/Phosphor) replacing the placeholder UI.
4. Plan 4 — Stealth auto-login (Python CloakBrowser sidecar + AutoDriver + bulk).

---

### Task 1: Classifiable errors (`api.APIError`, `Refresh` fallback, `store.ErrNotFound`)

The Refresher (Task 4) must tell a server-rejected refresh (→ `needs_relogin`) from a transient/network failure (→ `refresh_failed`), and callers must detect "account not found". This task adds the typed errors and a refresh-token fallback, all deferred from Plan 1.

**Files:**
- Modify: `internal/api/envelope.go`
- Modify: `internal/api/client.go` (the `Refresh` method only)
- Modify: `internal/store/sqlite.go` (the not-found error sites)
- Test: `internal/api/envelope_test.go` (add cases)
- Test: `internal/api/client_test.go` (add one case)
- Test: `internal/store/sqlite_test.go` (add one case)

**Interfaces:**
- Consumes: Plan 1 `decodeEnvelope`, `Client.Refresh`, `store` not-found sites.
- Produces:
  - `api.APIError{Code int; Msg string}` implementing `error` (message `"api error <code>: <msg>"`); `decodeEnvelope` returns `*api.APIError` for non-zero business codes.
  - `Client.Refresh` returns the *input* `refreshToken` when the API response omits/empties `refresh_token`.
  - `store.ErrNotFound` sentinel; `Get`/`UpdateTokens`/`SetStatus` return errors that satisfy `errors.Is(err, store.ErrNotFound)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/api/envelope_test.go`:
```go
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
```
Add `"errors"` to that file's imports.

Add to `internal/api/client_test.go`:
```go
func TestRefresh_KeepsOldRefreshTokenWhenResponseEmpty(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any) any {
		return map[string]any{"access_token": "Bearer new-a", "refresh_token": "", "refresh": false}
	})
	defer done()
	_, refresh, err := c.Refresh(context.Background(), "dev123", "Bearer old-a", "Bearer keep-r")
	if err != nil {
		t.Fatal(err)
	}
	if refresh != "Bearer keep-r" {
		t.Errorf("refresh = %q, want fallback to input", refresh)
	}
}
```

Add to `internal/store/sqlite_test.go`:
```go
func TestGet_MissingIsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("nobody@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: got %v, want ErrNotFound", err)
	}
	if err := s.SetStatus("nobody@example.com", StatusActive); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetStatus missing: got %v, want ErrNotFound", err)
	}
}
```
Add `"errors"` to that file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ ./internal/store/ -run 'TypedAPIError|KeepsOldRefresh|MissingIsErrNotFound' -v`
Expected: FAIL — `undefined: APIError`, refresh returns `""`, `undefined: ErrNotFound`.

- [ ] **Step 3: Add `APIError` to `internal/api/envelope.go`**

Replace the non-zero-code branch. The full file becomes:
```go
package api

import (
	"encoding/json"
	"fmt"
	"io"
)

// APIError is a non-zero business-level code returned in the response envelope.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return fmt.Sprintf("api error %d: %s", e.Code, e.Msg) }

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// decodeEnvelope reads the {code,msg,data} wrapper; a non-zero code yields *APIError.
// When out is non-nil, data is unmarshaled into it.
func decodeEnvelope(r io.Reader, out any) error {
	var e envelope
	if err := json.NewDecoder(r).Decode(&e); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	if e.Code != 0 {
		return &APIError{Code: e.Code, Msg: e.Msg}
	}
	if out != nil {
		return json.Unmarshal(e.Data, out)
	}
	return nil
}
```

- [ ] **Step 4: Add the refresh fallback in `internal/api/client.go`**

In the `Refresh` method, after decoding, fall back to the input token when the response is empty:
```go
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
```

- [ ] **Step 5: Add `ErrNotFound` in `internal/store/sqlite.go`**

Add the sentinel and use it at both not-found sites. Add `"errors"` to the imports, then:
```go
// ErrNotFound is returned when an account row does not exist.
var ErrNotFound = errors.New("account not found")
```
In `Get`, replace the not-found return:
```go
	if err == sql.ErrNoRows {
		return Account{}, fmt.Errorf("get %q: %w", email, ErrNotFound)
	}
```
In `mustAffect`, replace the zero-rows return:
```go
	if n == 0 {
		return fmt.Errorf("%q: %w", email, ErrNotFound)
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/api/ ./internal/store/ -v`
Expected: PASS (all api + store tests, including the new ones and the pre-existing `TestDecodeEnvelope_APIError` which still sees an error containing the code).

- [ ] **Step 7: Commit**

```bash
git add internal/api/ internal/store/
git commit -m "feat(api,store): typed APIError, refresh-token fallback, ErrNotFound sentinel"
```

---

### Task 2: Event bus (`internal/events`)

An in-process pub/sub so the AuthEngine and Refresher can push realtime updates to SSE subscribers without knowing about HTTP.

**Files:**
- Create: `internal/events/events.go`
- Test: `internal/events/events_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `events.Event{Type, Email, Detail string}` (json tags `type`,`email,omitempty`,`detail,omitempty`).
  - `events.Bus` with `func New() *Bus`, `func (b *Bus) Subscribe() (<-chan Event, func())` (channel + unsubscribe), `func (b *Bus) Publish(e Event)` (non-blocking; drops to a full subscriber rather than blocking).
  - Event type strings used across the app: `"login:ok"`, `"login:error"`, `"refresh:ok"`, `"refresh:failed"`, `"account:deleted"`.

- [ ] **Step 1: Write the failing test**

Create `internal/events/events_test.go`:
```go
package events

import (
	"testing"
	"time"
)

func TestPublishReachesSubscriber(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe()
	defer unsub()
	b.Publish(Event{Type: "login:ok", Email: "a@x.com"})
	select {
	case ev := <-ch:
		if ev.Type != "login:ok" || ev.Email != "a@x.com" {
			t.Errorf("ev = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe()
	unsub()
	b.Publish(Event{Type: "refresh:ok"})
	select {
	case _, open := <-ch:
		if open {
			t.Error("received event after unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		// no delivery is also acceptable
	}
}

func TestPublishDoesNotBlockOnFullSubscriber(t *testing.T) {
	b := New()
	_, unsub := b.Subscribe() // never drained
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Publish(Event{Type: "refresh:ok"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/events/ -v`
Expected: FAIL — `undefined: New` / `undefined: Event`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/events/events.go`:
```go
package events

import "sync"

// Event is a realtime notification pushed to dashboard subscribers.
type Event struct {
	Type   string `json:"type"`
	Email  string `json:"email,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Bus is an in-process fan-out pub/sub. Publish never blocks: a subscriber
// whose buffer is full drops the event.
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func New() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

// Subscribe returns a receive channel and an unsubscribe func that closes it.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, unsub
}

// Publish fans an event out to all current subscribers, skipping any whose
// buffer is full.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/events/ -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/events/
git commit -m "feat(events): in-process pub/sub bus for realtime updates"
```

---

### Task 3: AuthEngine + ManualDriver (`internal/auth`)

The login state machine: start a login (generate identity, get the OAuth URL, remember the pending `state`), then complete it when the browser redirect arrives at the callback (exchange code→tokens, persist the account, publish an event).

**Files:**
- Create: `internal/auth/auth.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `api.Client` (`GoogleOAuthURL`, `GoogleOAuthLogin`, `ParseClaims`), `identity.New`, `store.Store` (`Add`), `events.Bus`.
- Produces:
  - `auth.GoogleCred{Email, Password string}`.
  - `auth.LoginDriver` interface: `Drive(ctx context.Context, oauthURL string, cred *GoogleCred) error`.
  - `auth.ManualDriver{}` implementing it as a no-op (the UI opens the URL; the callback completes the flow).
  - `auth.Session{State, Status, Email, Err string; CreatedAt time.Time}` (json-friendly; `Status` ∈ `pending|ok|error`).
  - `auth.AuthEngine` with `func New(c *api.Client, st store.Store, bus *events.Bus) *AuthEngine`, `func (e *AuthEngine) StartLogin(ctx context.Context, driver LoginDriver, cred *GoogleCred) (state, oauthURL string, err error)`, `func (e *AuthEngine) HandleCallback(ctx context.Context, code, state string) error`, `func (e *AuthEngine) Status(state string) (Session, bool)`.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/auth_test.go`:
```go
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

// tok builds a minimal unsigned JWT ("Bearer h.<payload>.s") whose payload decodes
// to the given claims — so api.ParseClaims reads back exactly this jti/exp.
func tok(jti string, exp int64) string {
	payload, _ := json.Marshal(map[string]any{"jti": jti, "exp": exp, "device_id": "dev"})
	return "Bearer h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

var jwtA = tok("evmsnipe@gmail.com", 1783192918)

func newEngine(t *testing.T, autoglm http.HandlerFunc) (*AuthEngine, store.Store) {
	srv := httptest.NewServer(autoglm)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(api.NewClientWithBase(srv.URL), st, events.New()), st
}

func TestStartLogin_ReturnsStateAndURL(t *testing.T) {
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "https://accounts.google.com/o", "state": "st-1"}})
	})
	state, url, err := e.StartLogin(context.Background(), ManualDriver{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != "st-1" || url != "https://accounts.google.com/o" {
		t.Errorf("state=%q url=%q", state, url)
	}
	s, ok := e.Status("st-1")
	if !ok || s.Status != "pending" {
		t.Errorf("session = %+v ok=%v", s, ok)
	}
}

func TestHandleCallback_PersistsAccount(t *testing.T) {
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/userapi/overseasv1/google-oauth-url":
			data = map[string]any{"oauth_url": "u", "state": "st-2"}
		case "/userapi/overseasv1/google-oauth-login":
			data = map[string]any{"access_token": jwtA, "refresh_token": jwtA, "user_id": "hexid", "user_name": "EVM"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.HandleCallback(context.Background(), "auth-code", "st-2"); err != nil {
		t.Fatal(err)
	}
	acct, err := st.Get("evmsnipe@gmail.com")
	if err != nil {
		t.Fatalf("account not stored: %v", err)
	}
	if acct.AccessToken != jwtA || acct.Status != store.StatusActive || acct.DeviceID == "" {
		t.Errorf("acct = %+v", acct)
	}
	if s, _ := e.Status("st-2"); s.Status != "ok" || s.Email != "evmsnipe@gmail.com" {
		t.Errorf("session = %+v", s)
	}
}

func TestHandleCallback_UnknownState(t *testing.T) {
	e, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := e.HandleCallback(context.Background(), "c", "does-not-exist"); err == nil {
		t.Error("expected error for unknown state")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/auth/ -v`
Expected: FAIL — `undefined: New` / `undefined: ManualDriver` etc.

- [ ] **Step 3: Write minimal implementation**

Create `internal/auth/auth.go`:
```go
package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/identity"
	"gogoclaw/internal/store"
)

// GoogleCred is a one-time Google credential (used only by the Plan 4 auto driver).
type GoogleCred struct {
	Email    string
	Password string
}

// LoginDriver drives the Google consent step for a login. Manual = no-op (the UI
// opens the URL); auto (Plan 4) = a headless stealth browser.
type LoginDriver interface {
	Drive(ctx context.Context, oauthURL string, cred *GoogleCred) error
}

// ManualDriver does nothing — the user completes consent in their own browser and
// the redirect to the callback finishes the flow.
type ManualDriver struct{}

func (ManualDriver) Drive(ctx context.Context, oauthURL string, cred *GoogleCred) error { return nil }

// Session is the state of one in-flight or completed login.
type Session struct {
	State     string    `json:"state"`
	Status    string    `json:"status"` // pending | ok | error
	Email     string    `json:"email,omitempty"`
	Err       string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	identity identity.Identity
}

// AuthEngine coordinates login sessions.
type AuthEngine struct {
	api   *api.Client
	store store.Store
	bus   *events.Bus

	mu      sync.Mutex
	pending map[string]*Session
}

func New(c *api.Client, st store.Store, bus *events.Bus) *AuthEngine {
	return &AuthEngine{api: c, store: st, bus: bus, pending: make(map[string]*Session)}
}

// StartLogin generates a device identity, obtains the Google OAuth URL, records a
// pending session, and dispatches the driver. It returns immediately; the callback
// completes the flow. For ManualDriver the returned oauthURL is what the UI opens.
func (e *AuthEngine) StartLogin(ctx context.Context, driver LoginDriver, cred *GoogleCred) (string, string, error) {
	id, err := identity.New()
	if err != nil {
		return "", "", fmt.Errorf("generate identity: %w", err)
	}
	oauthURL, state, err := e.api.GoogleOAuthURL(ctx, id.DeviceID)
	if err != nil {
		return "", "", fmt.Errorf("oauth url: %w", err)
	}
	e.mu.Lock()
	e.pending[state] = &Session{State: state, Status: "pending", CreatedAt: time.Now(), identity: id}
	e.mu.Unlock()

	go func() {
		if err := driver.Drive(context.Background(), oauthURL, cred); err != nil {
			e.fail(state, fmt.Errorf("driver: %w", err))
		}
	}()
	return state, oauthURL, nil
}

// HandleCallback exchanges the OAuth code for tokens and persists the account.
func (e *AuthEngine) HandleCallback(ctx context.Context, code, state string) error {
	e.mu.Lock()
	sess, ok := e.pending[state]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown login state %q", state)
	}

	res, err := e.api.GoogleOAuthLogin(ctx, sess.identity.DeviceID, code, state)
	if err != nil {
		e.fail(state, err)
		return err
	}
	claims, err := api.ParseClaims(res.AccessToken)
	if err != nil {
		e.fail(state, fmt.Errorf("parse access token: %w", err))
		return err
	}
	rclaims, _ := api.ParseClaims(res.RefreshToken) // best-effort refresh expiry

	now := time.Now()
	acct := store.Account{
		Email: claims.Email, UserID: res.UserID, DeviceID: sess.identity.DeviceID,
		AccessToken: res.AccessToken, RefreshToken: res.RefreshToken,
		AccessExpiresAt: time.Unix(claims.Exp, 0), RefreshExpiresAt: time.Unix(rclaims.Exp, 0),
		PrivPEM: sess.identity.PrivPEM, PubPEM: sess.identity.PubPEM,
		AddedAt: now, LastRefreshedAt: now, Status: store.StatusActive,
	}
	if err := e.store.Add(acct); err != nil {
		e.fail(state, fmt.Errorf("store account: %w", err))
		return err
	}

	e.mu.Lock()
	sess.Status = "ok"
	sess.Email = claims.Email
	e.mu.Unlock()
	e.bus.Publish(events.Event{Type: "login:ok", Email: claims.Email})
	return nil
}

// Status returns a snapshot of a session (without the private identity).
func (e *AuthEngine) Status(state string) (Session, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sess, ok := e.pending[state]
	if !ok {
		return Session{}, false
	}
	snap := *sess
	snap.identity = identity.Identity{}
	return snap, true
}

func (e *AuthEngine) fail(state string, err error) {
	e.mu.Lock()
	if sess, ok := e.pending[state]; ok {
		sess.Status = "error"
		sess.Err = err.Error()
	}
	e.mu.Unlock()
	e.bus.Publish(events.Event{Type: "login:error", Detail: err.Error()})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add internal/auth/
git commit -m "feat(auth): AuthEngine login state machine + ManualDriver"
```

---

### Task 4: Refresher (`internal/refresh`)

Background token refresh: refresh accounts nearing access-token expiry, and classify failures — a server rejection (`*api.APIError`) means the refresh token is dead → `needs_relogin`; any other error (network/HTTP) is transient → `refresh_failed`.

**Files:**
- Create: `internal/refresh/refresh.go`
- Test: `internal/refresh/refresh_test.go`

**Interfaces:**
- Consumes: `api.Client` (`Refresh`, `ParseClaims`), `api.APIError`, `store.Store` (`List`,`Get`,`UpdateTokens`,`SetStatus`), `store.Status*`, `events.Bus`.
- Produces:
  - `refresh.Refresher` with `func New(c *api.Client, st store.Store, bus *events.Bus) *Refresher`.
  - `func (r *Refresher) RefreshOne(ctx context.Context, email string) error`.
  - `func (r *Refresher) RefreshAll(ctx context.Context) error`.
  - `func (r *Refresher) Run(ctx context.Context)` — ticks every `TickInterval` (1 min), refreshing due accounts; returns when ctx is cancelled.
  - Exported knobs (var, so tests can shrink them): `TickInterval time.Duration`, `RefreshMargin time.Duration`. A settable `Now func() time.Time` field (defaults to `time.Now`) for deterministic tests.

- [ ] **Step 1: Write the failing test**

Create `internal/refresh/refresh_test.go`:
```go
package refresh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

// tok builds a minimal unsigned JWT whose payload decodes to the given claims.
func tok(jti string, exp int64) string {
	payload, _ := json.Marshal(map[string]any{"jti": jti, "exp": exp, "device_id": "dev"})
	return "Bearer h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

var jwtNew = tok("a@example.com", 1799999999)

func seed(t *testing.T, st store.Store, aexp time.Time) {
	now := time.Now()
	err := st.Add(store.Account{
		Email: "a@example.com", UserID: "u", DeviceID: "dev",
		AccessToken: "Bearer old-a", RefreshToken: "Bearer old-r",
		AccessExpiresAt: aexp, RefreshExpiresAt: now.Add(720 * time.Hour),
		PrivPEM: "p", PubPEM: "P", AddedAt: now, LastRefreshedAt: now, Status: store.StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newRefresher(t *testing.T, h http.HandlerFunc) (*Refresher, store.Store) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(api.NewClientWithBase(srv.URL), st, events.New()), st
}

func TestRefreshOne_Success(t *testing.T) {
	r, st := newRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"access_token": jwtNew, "refresh_token": jwtNew}})
	})
	seed(t, st, time.Now().Add(time.Hour))
	if err := r.RefreshOne(context.Background(), "a@example.com"); err != nil {
		t.Fatal(err)
	}
	acct, _ := st.Get("a@example.com")
	if acct.AccessToken != jwtNew || acct.Status != store.StatusActive {
		t.Errorf("acct = %+v", acct)
	}
	if acct.AccessExpiresAt.Unix() != 1799999999 {
		t.Errorf("aexp = %d", acct.AccessExpiresAt.Unix())
	}
}

func TestRefreshOne_APIErrorMarksNeedsRelogin(t *testing.T) {
	r, st := newRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 40100, "msg": "refresh token expired", "data": nil})
	})
	seed(t, st, time.Now().Add(time.Hour))
	if err := r.RefreshOne(context.Background(), "a@example.com"); err == nil {
		t.Fatal("expected error")
	}
	acct, _ := st.Get("a@example.com")
	if acct.Status != store.StatusNeedsRelogin {
		t.Errorf("status = %q, want needs_relogin", acct.Status)
	}
}

func TestRefreshOne_TransientMarksRefreshFailed(t *testing.T) {
	r, st := newRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})
	seed(t, st, time.Now().Add(time.Hour))
	if err := r.RefreshOne(context.Background(), "a@example.com"); err == nil {
		t.Fatal("expected error")
	}
	acct, _ := st.Get("a@example.com")
	if acct.Status != store.StatusRefreshFailed {
		t.Errorf("status = %q, want refresh_failed", acct.Status)
	}
}

func TestRefreshDue_OnlyRefreshesExpiring(t *testing.T) {
	calls := 0
	r, st := newRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"access_token": jwtNew, "refresh_token": jwtNew}})
	})
	// Fixed "now"; account expires in 2 minutes, margin is 5 minutes → due.
	fixed := time.Unix(1_700_000_000, 0)
	r.Now = func() time.Time { return fixed }
	seed(t, st, fixed.Add(2*time.Minute))
	r.refreshDue(context.Background())
	if calls != 1 {
		t.Errorf("expected 1 refresh call, got %d", calls)
	}
	// A far-future account is not due.
	_ = st.Delete("a@example.com")
	seed(t, st, fixed.Add(48*time.Hour))
	calls = 0
	r.refreshDue(context.Background())
	if calls != 0 {
		t.Errorf("expected 0 refresh calls for non-due account, got %d", calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/refresh/ -v`
Expected: FAIL — `undefined: New` / `undefined: Refresher` / `r.refreshDue undefined`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/refresh/refresh.go`:
```go
package refresh

import (
	"context"
	"errors"
	"log"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

// Tunables (vars so tests can shrink them).
var (
	TickInterval  = time.Minute
	RefreshMargin = 5 * time.Minute
)

// Refresher keeps stored access tokens fresh.
type Refresher struct {
	api   *api.Client
	store store.Store
	bus   *events.Bus
	Now   func() time.Time
}

func New(c *api.Client, st store.Store, bus *events.Bus) *Refresher {
	return &Refresher{api: c, store: st, bus: bus, Now: time.Now}
}

// Run refreshes due accounts once immediately, then every TickInterval until ctx ends.
func (r *Refresher) Run(ctx context.Context) {
	r.refreshDue(ctx)
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.refreshDue(ctx)
		}
	}
}

// refreshDue refreshes every account whose access token is within RefreshMargin of
// expiry and is not already flagged needs_relogin.
func (r *Refresher) refreshDue(ctx context.Context) {
	accts, err := r.store.List()
	if err != nil {
		log.Printf("refresh: list accounts: %v", err)
		return
	}
	cutoff := r.Now().Add(RefreshMargin)
	for _, a := range accts {
		if a.Status == store.StatusNeedsRelogin {
			continue
		}
		if a.AccessExpiresAt.After(cutoff) {
			continue
		}
		if err := r.RefreshOne(ctx, a.Email); err != nil {
			log.Printf("refresh %s: %v", a.Email, err)
		}
	}
}

// RefreshOne refreshes a single account and classifies any failure.
func (r *Refresher) RefreshOne(ctx context.Context, email string) error {
	acct, err := r.store.Get(email)
	if err != nil {
		return err
	}
	newAccess, newRefresh, err := r.api.Refresh(ctx, acct.DeviceID, acct.AccessToken, acct.RefreshToken)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			_ = r.store.SetStatus(email, store.StatusNeedsRelogin)
			r.bus.Publish(events.Event{Type: "refresh:failed", Email: email, Detail: "needs_relogin"})
		} else {
			_ = r.store.SetStatus(email, store.StatusRefreshFailed)
			r.bus.Publish(events.Event{Type: "refresh:failed", Email: email, Detail: "transient"})
		}
		return err
	}
	ac, err := api.ParseClaims(newAccess)
	if err != nil {
		return err
	}
	rc, _ := api.ParseClaims(newRefresh)
	if err := r.store.UpdateTokens(email, newAccess, newRefresh, time.Unix(ac.Exp, 0), time.Unix(rc.Exp, 0)); err != nil {
		return err
	}
	r.bus.Publish(events.Event{Type: "refresh:ok", Email: email})
	return nil
}

// RefreshAll refreshes every stored account regardless of expiry.
func (r *Refresher) RefreshAll(ctx context.Context) error {
	accts, err := r.store.List()
	if err != nil {
		return err
	}
	for _, a := range accts {
		if err := r.RefreshOne(ctx, a.Email); err != nil {
			log.Printf("refresh-all %s: %v", a.Email, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/refresh/ -v`
Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add internal/refresh/
git commit -m "feat(refresh): background refresher with failure classification"
```

---

### Task 5: HTTP server, callback, SSE, embed (`internal/server`, `web/`)

The HTTP surface: static UI (embedded placeholder for now), the OAuth callback, the REST API, and the SSE stream. Account list responses expose no raw tokens.

**Files:**
- Create: `web/web.go` (embed)
- Create: `web/dist/index.html` (placeholder; Plan 3 replaces `web/dist` with the real build)
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `auth.AuthEngine` (`StartLogin`,`HandleCallback`,`Status`), `auth.ManualDriver`, `auth.GoogleCred`, `refresh.Refresher` (`RefreshOne`,`RefreshAll`), `store.Store` (`List`,`Delete`), `store.ErrNotFound`, `events.Bus` (`Subscribe`,`Publish`), `web.DistFS`.
- Produces:
  - `web.DistFS() fs.FS` — the embedded UI rooted at `dist`.
  - `server.Server` with `func New(engine *auth.AuthEngine, refresher *refresh.Refresher, st store.Store, bus *events.Bus) *Server` and `func (s *Server) Handler() http.Handler`.
  - Routes: `GET /auth/callback-google`, `POST /api/login/start`, `GET /api/login/status`, `GET /api/accounts`, `POST /api/accounts/{email}/refresh`, `POST /api/accounts/refresh-all`, `DELETE /api/accounts/{email}`, `GET /api/events`, static `GET /`.

- [ ] **Step 1: Create the embedded placeholder UI**

Create `web/dist/index.html`:
```html
<!doctype html>
<html lang="en">
  <head><meta charset="utf-8" /><title>GogoClaw</title></head>
  <body>
    <h1>GogoClaw</h1>
    <p>API is running. The dashboard UI ships in Plan 3.</p>
  </body>
</html>
```

Create `web/web.go`:
```go
// Package web embeds the built dashboard so the server ships as one binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// DistFS returns the built UI rooted at the dist directory.
func DistFS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this cannot fail at runtime
	}
	return sub
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/server/server_test.go`:
```go
package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogoclaw/internal/api"
	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/store"
)

// tok builds a minimal unsigned JWT whose payload decodes to the given claims.
func tok(jti string, exp int64) string {
	payload, _ := json.Marshal(map[string]any{"jti": jti, "exp": exp, "device_id": "dev"})
	return "Bearer h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

var jwtA = tok("evmsnipe@gmail.com", 1783192918)

// newServer wires a Server whose api.Client points at a mock AutoGLM.
func newServer(t *testing.T, autoglm http.HandlerFunc) (http.Handler, store.Store) {
	srv := httptest.NewServer(autoglm)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := api.NewClientWithBase(srv.URL)
	bus := events.New()
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus).Handler(), st
}

func TestLoginStart_ReturnsStateAndURL(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "https://g/o", "state": "st-1"}})
	})
	req := httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"manual"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["state"] != "st-1" || out["oauth_url"] != "https://g/o" {
		t.Errorf("out = %v", out)
	}
}

func TestCallback_CompletesLoginAndListsAccount(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/userapi/overseasv1/google-oauth-url":
			data = map[string]any{"oauth_url": "u", "state": "st-2"}
		case "/userapi/overseasv1/google-oauth-login":
			data = map[string]any{"access_token": jwtA, "refresh_token": jwtA, "user_id": "hexid"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	// start
	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"manual"}`)))
	// browser hits callback
	cb := httptest.NewRecorder()
	h.ServeHTTP(cb, httptest.NewRequest("GET", "/auth/callback-google?code=abc&state=st-2", nil))
	if cb.Code != 200 {
		t.Fatalf("callback code = %d", cb.Code)
	}
	// list shows the account and no raw token
	list := httptest.NewRecorder()
	h.ServeHTTP(list, httptest.NewRequest("GET", "/api/accounts", nil))
	body := list.Body.String()
	if !strings.Contains(body, "evmsnipe@gmail.com") {
		t.Errorf("account missing from list: %s", body)
	}
	if strings.Contains(body, jwtA) || strings.Contains(body, "Bearer ") {
		t.Errorf("list leaked a raw bearer token: %s", body)
	}
}

func TestDeleteAccount(t *testing.T) {
	h, st := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	_ = st.Add(store.Account{Email: "d@x.com", Status: store.StatusActive})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/accounts/d@x.com", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	if _, err := st.Get("d@x.com"); err == nil {
		t.Error("account not deleted")
	}
}

func TestServesPlaceholderUI(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "GogoClaw") {
		t.Errorf("placeholder UI not served: code=%d body=%s", rec.Code, rec.Body)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/server/ -v`
Expected: FAIL — `undefined: New` / `undefined: Handler`.

- [ ] **Step 4: Write minimal implementation**

Create `internal/server/server.go`:
```go
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/store"
	"gogoclaw/web"
)

// Server owns the HTTP surface.
type Server struct {
	engine    *auth.AuthEngine
	refresher *refresh.Refresher
	store     store.Store
	bus       *events.Bus
}

func New(engine *auth.AuthEngine, refresher *refresh.Refresher, st store.Store, bus *events.Bus) *Server {
	return &Server{engine: engine, refresher: refresher, store: st, bus: bus}
}

// Handler builds the route mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback-google", s.handleCallback)
	mux.HandleFunc("POST /api/login/start", s.handleLoginStart)
	mux.HandleFunc("GET /api/login/status", s.handleLoginStatus)
	mux.HandleFunc("GET /api/accounts", s.handleAccounts)
	mux.HandleFunc("POST /api/accounts/refresh-all", s.handleRefreshAll)
	mux.HandleFunc("POST /api/accounts/{email}/refresh", s.handleRefreshOne)
	mux.HandleFunc("DELETE /api/accounts/{email}", s.handleDelete)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.Handle("/", http.FileServerFS(web.DistFS()))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string           `json:"mode"`
		Cred *auth.GoogleCred `json:"cred"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Mode == "auto" {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auto login arrives in Plan 4"})
		return
	}
	state, url, err := s.engine.StartLogin(r.Context(), auth.ManualDriver{}, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": state, "oauth_url": url})
}

func (s *Server) handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.engine.Status(r.URL.Query().Get("state"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown state"})
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	w.Header().Set("content-type", "text/html; charset=utf-8")
	if err := s.engine.HandleCallback(r.Context(), code, state); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "<h1>Login failed</h1><p>%s</p>", err.Error())
		return
	}
	_, _ = w.Write([]byte("<h1>Login successful</h1><p>You can close this tab.</p>"))
}

type accountView struct {
	Email            string `json:"email"`
	UserID           string `json:"user_id"`
	Status           string `json:"status"`
	AccessExpiresAt  int64  `json:"access_expires_at"`
	RefreshExpiresAt int64  `json:"refresh_expires_at"`
	LastRefreshedAt  int64  `json:"last_refreshed_at"`
	AddedAt          int64  `json:"added_at"`
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	accts, err := s.store.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]accountView, 0, len(accts))
	for _, a := range accts {
		out = append(out, accountView{
			Email: a.Email, UserID: a.UserID, Status: a.Status,
			AccessExpiresAt: a.AccessExpiresAt.Unix(), RefreshExpiresAt: a.RefreshExpiresAt.Unix(),
			LastRefreshedAt: a.LastRefreshedAt.Unix(), AddedAt: a.AddedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRefreshOne(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	if err := s.refresher.RefreshOne(r.Context(), email); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	if err := s.refresher.RefreshAll(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	if err := s.store.Delete(email); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.bus.Publish(events.Event{Type: "account:deleted", Email: email})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	ch, unsub := s.bus.Subscribe()
	defer unsub()
	// Initial comment so clients (and tests) know the subscription is live.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/server/ ./web/ -v`
Expected: PASS (all server tests; `web` has no tests but must compile).

- [ ] **Step 6: Add an SSE delivery test**

Append to `internal/server/server_test.go`:
```go
func TestSSE_DeliversPublishedEvent(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 256)
	// Read the ": connected" comment first — guarantees the subscription is live.
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("reading connect comment: %v", err)
	}
	// Now publish; the handler is subscribed, so this reaches it.
	// Reach the bus via a second account delete through the API.
	del := httptest.NewRecorder()
	h.ServeHTTP(del, httptest.NewRequest("DELETE", "/api/accounts/ghost@x.com", nil))

	buf2 := make([]byte, 256)
	n, err := resp.Body.Read(buf2)
	if err != nil {
		t.Fatalf("reading event: %v", err)
	}
	if !strings.Contains(string(buf2[:n]), "account:deleted") {
		t.Errorf("expected account:deleted event, got %q", string(buf2[:n]))
	}
}
```

Run: `go test ./internal/server/ -run TestSSE -v`
Expected: PASS. (Deleting a non-existent email still publishes `account:deleted` because SQLite `DELETE` of a missing row is not an error.)

- [ ] **Step 7: Commit**

```bash
git add web/ internal/server/
git commit -m "feat(server): REST + callback + SSE HTTP surface with embedded UI"
```

---

### Task 6: Main wiring + end-to-end (`cmd/gogoclaw/main.go`)

Assemble everything into a runnable binary bound to loopback, with the refresher running in the background and graceful shutdown.

**Files:**
- Modify: `cmd/gogoclaw/main.go` (currently absent; create it)
- Test: `cmd/gogoclaw/main_test.go`

**Interfaces:**
- Consumes: `store.Open`, `api.NewClient`, `events.New`, `auth.New`, `refresh.New` + `refresh.Refresher.Run`, `server.New` + `server.Server.Handler`.
- Produces: `func buildHandler(st store.Store, c *api.Client, bus *events.Bus) (http.Handler, *refresh.Refresher)` (testable wiring) and `func main()` (opens the DB, starts the refresher, serves on `127.0.0.1:18432`, shuts down on SIGINT).

- [ ] **Step 1: Write the failing test**

Create `cmd/gogoclaw/main_test.go`:
```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

func TestBuildHandler_ServesUIAndAPI(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, refresher := buildHandler(st, api.NewClient(), events.New())
	if refresher == nil {
		t.Fatal("refresher not built")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Errorf("UI route code = %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/api/accounts", nil))
	if rec2.Code != 200 || rec2.Body.String() == "" {
		t.Errorf("accounts route code = %d body = %s", rec2.Code, rec2.Body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/gogoclaw/ -v`
Expected: FAIL — `undefined: buildHandler`.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/gogoclaw/main.go`:
```go
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"gogoclaw/internal/api"
	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/server"
	"gogoclaw/internal/store"
)

const addr = "127.0.0.1:18432"

// buildHandler wires the app graph and returns the HTTP handler plus the refresher
// (whose Run loop the caller starts).
func buildHandler(st store.Store, c *api.Client, bus *events.Bus) (http.Handler, *refresh.Refresher) {
	engine := auth.New(c, st, bus)
	refresher := refresh.New(c, st, bus)
	srv := server.New(engine, refresher, st, bus)
	return srv.Handler(), refresher
}

func main() {
	st, err := store.Open("accounts.db")
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	bus := events.New()
	handler, refresher := buildHandler(st, api.NewClient(), bus)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go refresher.Run(ctx)

	httpSrv := &http.Server{Addr: addr, Handler: handler}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	log.Printf("GogoClaw listening on http://%s", addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
	log.Println("GogoClaw stopped")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/gogoclaw/ -v`
Expected: PASS.

- [ ] **Step 5: Build + full suite**

Run:
```bash
cd /e/autoclaw && go build ./... && go vet ./... && go test ./... -count=1
```
Expected: build succeeds, vet clean, all packages PASS.

- [ ] **Step 6: Manual smoke (documented; not automated)**

Run: `go run ./cmd/gogoclaw` then, in another shell, `curl -s http://127.0.0.1:18432/ | grep GogoClaw` and `curl -s http://127.0.0.1:18432/api/accounts`.
Expected: the placeholder HTML contains "GogoClaw"; `/api/accounts` returns `[]` (or existing accounts). Ctrl-C stops the server cleanly (logs "GogoClaw stopped"). A real Google login can be exercised by POSTing `{"mode":"manual"}` to `/api/login/start` and opening the returned `oauth_url`.

- [ ] **Step 7: Commit**

```bash
git add cmd/gogoclaw/
git commit -m "feat(cmd): wire server + refresher into loopback binary"
```

---

## Notes for the implementer

- **`buildHandler` is the seam** for testing wiring without binding a port; `main` binds `127.0.0.1:18432` (loopback per the global constraint — never `0.0.0.0`).
- **`accountView` deliberately omits tokens.** The list endpoint must never serialize `AccessToken`/`RefreshToken`; a test asserts no `eyJhbGci` (JWT header) leaks.
- **Refresh classification hinges on Task 1's `*api.APIError`.** A server business-code rejection ⇒ `needs_relogin`; transport/HTTP-status errors ⇒ `refresh_failed`. Don't collapse the two.
- **The callback needs no state beyond the pending map** — the browser (manual user or Plan 4's sidecar) simply GETs `/auth/callback-google?code&state`, and `HandleCallback` does the exchange. This is the shared convergence point for both drivers.
- **`web/dist` is a placeholder** this plan; Plan 3 replaces the directory with the Vite build output. Keep the `web.DistFS()` contract stable so the server code doesn't change.
- Deferred to later: exposing tokens via a dedicated "reveal/copy" endpoint, and SPA-fallback routing (serve `index.html` for unknown non-API paths) — add in Plan 3 when the SPA needs client-side routes.
