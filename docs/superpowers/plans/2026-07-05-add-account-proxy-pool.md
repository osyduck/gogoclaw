# Add-Account Proxy Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route the AutoGLM API calls made while adding an account (and only then) through a dedicated, persistent pool of HTTP proxies, auto-failing-over to the next proxy on error 630014.

**Architecture:** A new single-row SQLite table stores the proxy pool. `api.Client` gains a `WithProxy` variant that routes through a proxy. The auth engine, when a login is started with the pool toggle, snapshots a rotated proxy order onto the session and drives every AutoGLM call (`OAuthURL`, `OAuthLogin`, `Wallets` seed) through a `tryVia` failover helper. The stealth browser, the background refresher, and the LLM gateway are untouched. The dashboard gets a pool editor panel and the Bulk login dialog gets a per-batch toggle.

**Tech Stack:** Go (stdlib `net/http`, `net/url`; pure-Go `modernc.org/sqlite`); React + TypeScript + Tailwind + `@tanstack/react-query` + `@phosphor-icons/react`; Vitest + Testing Library.

## Global Constraints

- Go module path: `gogoclaw`. Go standard library only for new backend code (no new deps).
- SQLite driver: `modernc.org/sqlite` (already imported). Schema changes go in the additive `migrations` slice in `internal/store/sqlite.go`; `CREATE TABLE IF NOT EXISTS` (not `ADD COLUMN`) so re-open is idempotent.
- `store.Store` is implemented only by `*store.SQLiteStore`; other packages depend on narrower subset interfaces, so adding methods to `store.Store` only requires updating `SQLiteStore`.
- `GoogleCred` fields have no JSON tags and rely on Go's case-insensitive JSON matching (`email` → `Email`). Keep that convention.
- Frontend backend calls go through the `req()` helper in `web/src/lib/api.ts`. React-query keys are kebab-case strings.
- Go tests: `go test ./internal/...` from repo root. Frontend tests: `cd web && npx vitest run <file>` (the `test` script is `vitest run`).
- Proxy URLs are shown/edited in plaintext (localhost single-user dashboard); do not mask them.
- The proxy is reached ONLY on a login session started with the pool toggle on. Never proxy the refresher, the gateway, or the browser.

---

### Task 1: Store — persist the login proxy pool

**Files:**
- Modify: `internal/store/store.go` (add two methods to the `Store` interface)
- Modify: `internal/store/sqlite.go` (migration + implementations)
- Test: `internal/store/sqlite_test.go`

**Interfaces:**
- Produces:
  - `store.Store.GetLoginProxies() ([]string, error)` — returns the pool (empty slice when unset), blank lines dropped, order preserved.
  - `store.Store.SetLoginProxies([]string) error` — replaces the pool.

- [ ] **Step 1: Write the failing tests**

Add to `internal/store/sqlite_test.go`:

```go
func TestLoginProxies_RoundTrip(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetLoginProxies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("default = %v, want empty", got)
	}

	want := []string{"http://u:p@1.2.3.4:8080", "socks5://5.6.7.8:1080"}
	if err := s.SetLoginProxies(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetLoginProxies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("round-trip = %v, want %v", got, want)
	}
}

func TestLoginProxies_DropsBlankLines(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetLoginProxies([]string{"http://a:8080", "", "   "}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetLoginProxies()
	if len(got) != 1 || got[0] != "http://a:8080" {
		t.Fatalf("got %v, want one non-blank entry", got)
	}
}

func TestOpen_MigratesLoginProxyPoolTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (
	  email TEXT PRIMARY KEY, user_id TEXT NOT NULL, device_id TEXT NOT NULL,
	  access_token TEXT NOT NULL, refresh_token TEXT NOT NULL,
	  access_expires_at INTEGER NOT NULL, refresh_expires_at INTEGER NOT NULL,
	  priv_pem TEXT NOT NULL, pub_pem TEXT NOT NULL,
	  added_at INTEGER NOT NULL, last_refreshed_at INTEGER NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer s.Close()
	if _, err := s.GetLoginProxies(); err != nil {
		t.Fatalf("GetLoginProxies after migration: %v", err)
	}
	if err := s.SetLoginProxies([]string{"http://a:8080"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetLoginProxies(); len(got) != 1 {
		t.Errorf("got %v, want 1", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run TestLoginProxies -v`
Expected: compile failure — `s.GetLoginProxies undefined` / `s.SetLoginProxies undefined`.

- [ ] **Step 3: Add the two methods to the Store interface**

In `internal/store/store.go`, inside the `Store` interface (after `SetProxyConfig(ProxyConfig) error`):

```go
	GetLoginProxies() ([]string, error)
	SetLoginProxies([]string) error
```

- [ ] **Step 4: Add the migration**

In `internal/store/sqlite.go`, append to the `migrations` slice (after the `proxy_settings` entry):

```go
	`CREATE TABLE IF NOT EXISTS login_proxy_pool (
	  id      INTEGER PRIMARY KEY CHECK (id = 1),
	  proxies TEXT NOT NULL
	)`,
```

- [ ] **Step 5: Implement the methods**

Add to `internal/store/sqlite.go` (after `SetProxyConfig`). `strings` is already imported.

```go
func (s *SQLiteStore) GetLoginProxies() ([]string, error) {
	row := s.db.QueryRow(`SELECT proxies FROM login_proxy_pool WHERE id = 1`)
	var raw string
	err := row.Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *SQLiteStore) SetLoginProxies(proxies []string) error {
	raw := strings.Join(proxies, "\n")
	_, err := s.db.Exec(`
INSERT INTO login_proxy_pool (id, proxies) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET proxies=excluded.proxies`, raw)
	return err
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/store/ -run 'TestLoginProxies|TestOpen_MigratesLoginProxyPoolTable' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/store/store.go internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): persist a login proxy pool"
```

---

### Task 2: API client — per-proxy variant + 630014 constant

**Files:**
- Modify: `internal/api/client.go` (add `WithProxy`)
- Modify: `internal/api/envelope.go` (add `CodeVerificationFailed`)
- Test: `internal/api/client_test.go`, `internal/api/envelope_test.go`

**Interfaces:**
- Produces:
  - `func (c *api.Client) WithProxy(proxyURL string) (*api.Client, error)` — returns a shallow copy whose HTTP transport proxies through `proxyURL`; empty string returns the receiver; malformed URL errors.
  - `const api.CodeVerificationFailed = 630014`

- [ ] **Step 1: Write the failing tests**

Add to `internal/api/client_test.go` (imports `net/http`, `net/http/httptest` are already present):

```go
func TestWithProxy_SetsProxiedTransport(t *testing.T) {
	c := NewClient()
	pc, err := c.WithProxy("http://user:pass@127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := pc.http.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("expected an *http.Transport with a Proxy func")
	}
	u, err := tr.Proxy(httptest.NewRequest("GET", "http://autoglm-api.autoglm.ai/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "127.0.0.1:8080" {
		t.Errorf("proxy = %v, want host 127.0.0.1:8080", u)
	}
}

func TestWithProxy_EmptyReturnsReceiver(t *testing.T) {
	c := NewClient()
	pc, err := c.WithProxy("")
	if err != nil || pc != c {
		t.Errorf("empty proxy: got (%p,%v), want receiver unchanged", pc, err)
	}
}

func TestWithProxy_InvalidURLErrors(t *testing.T) {
	c := NewClient()
	if _, err := c.WithProxy("http://%zz"); err == nil {
		t.Error("expected error for malformed proxy url")
	}
}
```

Add to `internal/api/envelope_test.go` (add `errors` and `strings` to its imports if missing):

```go
func TestDecodeEnvelope_SurfacesVerificationFailedCode(t *testing.T) {
	err := decodeEnvelope(strings.NewReader(`{"code":630014,"msg":"Verification failed","data":null}`), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeVerificationFailed {
		t.Fatalf("err = %v, want *APIError with code %d", err, CodeVerificationFailed)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/api/ -run 'TestWithProxy|TestDecodeEnvelope_Surfaces' -v`
Expected: compile failure — `WithProxy` and `CodeVerificationFailed` undefined.

- [ ] **Step 3: Add the constant**

In `internal/api/envelope.go`, after the `APIError` type declaration (before `type envelope struct`):

```go
// CodeVerificationFailed is the AutoGLM business code returned when the caller's
// IP is rejected during OAuth ("Verification failed"). Used to trigger proxy
// failover when adding an account.
const CodeVerificationFailed = 630014
```

- [ ] **Step 4: Implement WithProxy**

In `internal/api/client.go`, add `"net/url"` to the imports, then add after `NewClientWithBase`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/api/ -run 'TestWithProxy|TestDecodeEnvelope_Surfaces' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/client.go internal/api/envelope.go internal/api/client_test.go internal/api/envelope_test.go
git commit -m "feat(api): proxied client variant and 630014 constant"
```

---

### Task 3: Auth — proxy-pool failover in the login flow

**Files:**
- Modify: `internal/auth/auth.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `api.Client.WithProxy`, `api.CodeVerificationFailed`, `api.APIError` (Task 2); `store.Store.GetLoginProxies` (Task 1).
- Produces:
  - `auth.GoogleCred` gains `UseProxyPool bool`.
  - `(*AuthEngine).tryVia(order []string, onStep func(string), call func(*api.Client) error) error` — runs `call` through each proxy in `order` until success or a non-retryable error; `nil`/empty `order` calls once with the plain client.
  - `(*AuthEngine).nextProxyOrder() []string` — snapshots the pool rotated by a round-robin cursor.

- [ ] **Step 1: Write the failing tests**

Add to `internal/auth/auth_test.go` (imports `net/http`, `net/http/httptest`, `context`, `errors`, `gogoclaw/internal/api` are already present):

```go
func TestTryVia_NoPoolCallsPlainClientOnce(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	var attempts int
	err := e.tryVia(nil, nil, func(*api.Client) error { attempts++; return nil })
	if err != nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v, want 1,nil", attempts, err)
	}
}

func TestTryVia_FailsOverOn630014(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	order := []string{"http://p1:8080", "http://p2:8080", "http://p3:8080"}
	var attempts int
	err := e.tryVia(order, nil, func(*api.Client) error {
		attempts++
		if attempts < 2 {
			return &api.APIError{Code: api.CodeVerificationFailed, Msg: "Verification failed"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want success after one failover, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts=%d, want 2", attempts)
	}
}

func TestTryVia_TerminalErrorStopsImmediately(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	order := []string{"http://p1:8080", "http://p2:8080"}
	var attempts int
	err := e.tryVia(order, nil, func(*api.Client) error {
		attempts++
		return &api.APIError{Code: 400001, Msg: "bad request"}
	})
	if err == nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v, want 1 and terminal error", attempts, err)
	}
}

func TestTryVia_ExhaustsPoolThenReturnsLastError(t *testing.T) {
	e := New(api.NewClient(), nil, nil)
	order := []string{"http://p1:8080", "http://p2:8080"}
	var attempts int
	err := e.tryVia(order, nil, func(*api.Client) error {
		attempts++
		return &api.APIError{Code: api.CodeVerificationFailed, Msg: "Verification failed"}
	})
	if err == nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v, want 2 and the last error", attempts, err)
	}
}

func TestNextProxyOrder_RoundRobinRotation(t *testing.T) {
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := st.SetLoginProxies([]string{"http://a:1", "http://b:2", "http://c:3"}); err != nil {
		t.Fatal(err)
	}
	first := e.nextProxyOrder()
	second := e.nextProxyOrder()
	if len(first) != 3 || first[0] != "http://a:1" {
		t.Fatalf("first order = %v", first)
	}
	if len(second) != 3 || second[0] != "http://b:2" {
		t.Fatalf("second order = %v, want to start at the next proxy", second)
	}
}

// TestStartLogin_ProxyPoolRoutesThroughProxy proves the toggle actually routes
// AutoGLM calls through the pool: with a single unreachable proxy, the OAuthURL
// call fails (so StartLogin errors), whereas the same flow with the pool off
// succeeds via the direct base server.
func TestStartLogin_ProxyPoolRoutesThroughProxy(t *testing.T) {
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-p"}})
	})
	if err := st.SetLoginProxies([]string{"http://127.0.0.1:1"}); err != nil { // refused
		t.Fatal(err)
	}

	// Pool ON → routed through the dead proxy → StartLogin fails.
	on := &GoogleCred{Email: "a@x.com", Password: "pw", UseProxyPool: true}
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, on); err == nil {
		t.Error("want error when routing through an unreachable proxy")
	}

	// Pool OFF → direct base server → StartLogin succeeds.
	off := &GoogleCred{Email: "b@x.com", Password: "pw", UseProxyPool: false}
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, off); err != nil {
		t.Errorf("pool off should use the direct base: %v", err)
	}
}
```

Note: `newEngine` already exists and returns `(*AuthEngine, store.Store)`. `New(api.NewClient(), nil, nil)` is safe for the `tryVia` unit tests because they never touch the store or bus.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/ -run 'TestTryVia|TestNextProxyOrder|TestStartLogin_ProxyPool' -v`
Expected: compile failure — `UseProxyPool`, `tryVia`, `nextProxyOrder` undefined.

- [ ] **Step 3: Add the field, engine cursor, and helpers**

In `internal/auth/auth.go`, add `"net/url"` to the imports.

Extend `GoogleCred`:

```go
type GoogleCred struct {
	Email        string
	Password     string
	Proxy        string // optional residential proxy; empty = none
	UseProxyPool bool   // route this account's AutoGLM API calls through the login proxy pool
}
```

Add an unexported field to `Session` (after `provider api.Provider`):

```go
	order    []string // proxy failover order for this login (nil = no pool)
```

Add a cursor field to `AuthEngine` (after `pending map[string]*Session`):

```go
	poolCursor int // round-robin index into the login proxy pool
```

Add the helpers (anywhere in the file, e.g. after `fail`):

```go
// nextProxyOrder snapshots the login proxy pool rotated by a round-robin cursor,
// so successive accounts in a batch start on different proxies. Returns nil when
// the pool is empty or unreadable (the flow then runs unproxied).
func (e *AuthEngine) nextProxyOrder() []string {
	proxies, err := e.store.GetLoginProxies()
	if err != nil || len(proxies) == 0 {
		return nil
	}
	e.mu.Lock()
	start := e.poolCursor % len(proxies)
	e.poolCursor++
	e.mu.Unlock()
	order := make([]string, 0, len(proxies))
	for i := 0; i < len(proxies); i++ {
		order = append(order, proxies[(start+i)%len(proxies)])
	}
	return order
}

// tryVia runs call, routing AutoGLM requests through each proxy in order until
// one succeeds or a non-retryable error occurs. An empty order calls once with
// the plain (direct) client. onStep, when non-nil, receives a progress line per
// attempt so the UI can show failover live.
func (e *AuthEngine) tryVia(order []string, onStep func(string), call func(*api.Client) error) error {
	if len(order) == 0 {
		return call(e.api)
	}
	var lastErr error
	for i, p := range order {
		client, err := e.api.WithProxy(p)
		if err != nil {
			lastErr = err
			continue
		}
		if onStep != nil {
			onStep(fmt.Sprintf("attempt %d/%d via proxy %s", i+1, len(order), proxyHost(p)))
		}
		lastErr = call(client)
		if lastErr == nil {
			return nil
		}
		if !retryable(lastErr) {
			return lastErr
		}
		if onStep != nil {
			onStep(fmt.Sprintf("proxy %s rejected (%v) — trying next", proxyHost(p), lastErr))
		}
	}
	return lastErr
}

// retryable reports whether an error should trigger failover to the next proxy:
// a 630014 verification failure (flagged IP), or a transport-level error (dead
// proxy). Any other business error is terminal.
func retryable(err error) bool {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == api.CodeVerificationFailed
	}
	return true
}

// proxyHost returns the host:port of a proxy URL for display, dropping any
// embedded credentials. Falls back to the raw string if it doesn't parse.
func proxyHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}
```

- [ ] **Step 4: Route OAuthURL in StartLogin through the pool**

In `StartLogin`, replace the identity/OAuthURL/session preamble. Change from:

```go
	id, err := identity.New()
	if err != nil {
		return "", "", fmt.Errorf("generate identity: %w", err)
	}
	oauthURL, state, err := e.api.OAuthURL(ctx, provider, id.DeviceID)
	if err != nil {
		return "", "", fmt.Errorf("oauth url: %w", err)
	}
	sess := &Session{State: state, Status: "pending", CreatedAt: time.Now(), identity: id, provider: provider}
```

to:

```go
	id, err := identity.New()
	if err != nil {
		return "", "", fmt.Errorf("generate identity: %w", err)
	}
	var order []string
	if cred != nil && cred.UseProxyPool {
		order = e.nextProxyOrder()
	}
	var oauthURL, state string
	if err := e.tryVia(order, nil, func(c *api.Client) error {
		var e2 error
		oauthURL, state, e2 = c.OAuthURL(ctx, provider, id.DeviceID)
		return e2
	}); err != nil {
		return "", "", fmt.Errorf("oauth url: %w", err)
	}
	sess := &Session{State: state, Status: "pending", CreatedAt: time.Now(), identity: id, provider: provider, order: order}
```

- [ ] **Step 5: Route OAuthLogin and the Wallets seed in HandleCallback through the pool**

In `HandleCallback`, replace the exchange call. Change from:

```go
	res, err := e.api.OAuthLogin(ctx, provider, sess.identity.DeviceID, code, state)
	if err != nil {
		e.fail(state, err)
		return err
	}
```

to:

```go
	step := func(line string) {
		e.mu.Lock()
		if s, ok := e.pending[state]; ok {
			s.Steps = append(s.Steps, line)
		}
		e.mu.Unlock()
	}
	var res api.LoginResult
	err := e.tryVia(sess.order, step, func(c *api.Client) error {
		var e2 error
		res, e2 = c.OAuthLogin(ctx, provider, sess.identity.DeviceID, code, state)
		return e2
	})
	if err != nil {
		e.fail(state, err)
		return err
	}
```

Then replace the Wallets seed. Change from:

```go
	if w, err := e.api.Wallets(ctx, res.AccessToken); err != nil {
		log.Printf("balance %s: %v", claims.Email, err)
	} else if err := e.store.UpdateBalance(claims.Email, w.TotalBalance); err != nil {
		log.Printf("balance %s: store: %v", claims.Email, err)
	}
```

to:

```go
	var w api.Wallet
	if err := e.tryVia(sess.order, nil, func(c *api.Client) error {
		var e2 error
		w, e2 = c.Wallets(ctx, res.AccessToken)
		return e2
	}); err != nil {
		log.Printf("balance %s: %v", claims.Email, err)
	} else if err := e.store.UpdateBalance(claims.Email, w.TotalBalance); err != nil {
		log.Printf("balance %s: store: %v", claims.Email, err)
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS (new proxy tests plus all existing callback/step tests still green).

- [ ] **Step 7: Commit**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go
git commit -m "feat(auth): proxy-pool failover for add-account AutoGLM calls"
```

---

### Task 4: Server — pool endpoints + bulk toggle

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `store.Store.GetLoginProxies`/`SetLoginProxies` (Task 1); `auth.GoogleCred.UseProxyPool` (Task 3).
- Produces: `GET /api/login/proxy-pool` → `{"proxies":[...],"count":N}`; `POST /api/login/proxy-pool` with `{"proxies":[...]}`; `POST /api/login/bulk` accepts `"use_proxy_pool"`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/server/server_test.go`. A small helper builds a mock that answers oauth-url:

```go
func okOAuthURL(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
		"data": map[string]any{"oauth_url": "u", "state": "st-" + r.URL.Path}})
}

func TestLoginProxyPool_GetSetRoundTrip(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/proxy-pool",
		strings.NewReader(`{"proxies":["http://u:p@1.2.3.4:8080"," "," socks5://5.6.7.8:1080 "]}`)))
	if rec.Code != 200 {
		t.Fatalf("POST code = %d body = %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/login/proxy-pool", nil))
	if rec.Code != 200 {
		t.Fatalf("GET code = %d", rec.Code)
	}
	var out struct {
		Proxies []string `json:"proxies"`
		Count   int      `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Count != 2 || out.Proxies[0] != "http://u:p@1.2.3.4:8080" || out.Proxies[1] != "socks5://5.6.7.8:1080" {
		t.Errorf("pool = %+v, want 2 trimmed entries", out)
	}
}

func TestLoginProxyPool_RejectsInvalidURL(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/proxy-pool",
		strings.NewReader(`{"proxies":["ftp://nope"]}`)))
	if rec.Code != 400 {
		t.Errorf("code = %d, want 400 for a bad scheme", rec.Code)
	}
}

// TestBulkLogin_UsesProxyPoolWhenFlagSet proves use_proxy_pool threads all the
// way through: with a single unreachable proxy configured, an account started
// with the flag fails (routed through the dead proxy), while the same account
// without the flag starts fine via the direct base server.
func TestBulkLogin_UsesProxyPoolWhenFlagSet(t *testing.T) {
	al := &fakeAutoLogin{driver: stubDriver{}}
	h := newServerWithAuto(t, okOAuthURL, al)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/proxy-pool",
		strings.NewReader(`{"proxies":["http://127.0.0.1:1"]}`)))
	if rec.Code != 200 {
		t.Fatalf("configure pool: code = %d body = %s", rec.Code, rec.Body)
	}

	decode := func(body string) (started, errs int) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/bulk", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("bulk code = %d body = %s", rec.Code, rec.Body)
		}
		var out struct {
			Started []struct{ Email string } `json:"started"`
			Errors  []struct{ Email string } `json:"errors"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return len(out.Started), len(out.Errors)
	}

	// Flag on → routed through the dead proxy → the account errors.
	if s, e := decode(`{"accounts":[{"email":"a@x.com","password":"p"}],"use_proxy_pool":true}`); s != 0 || e != 1 {
		t.Errorf("flag on: started=%d errors=%d, want 0/1", s, e)
	}
	// Flag off → direct base server → the account starts.
	if s, e := decode(`{"accounts":[{"email":"b@x.com","password":"p"}],"use_proxy_pool":false}`); s != 1 || e != 0 {
		t.Errorf("flag off: started=%d errors=%d, want 1/0", s, e)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/ -run 'TestLoginProxyPool|TestBulkLogin_UsesProxyPool' -v`
Expected: FAIL — routes 404 and `use_proxy_pool` ignored (flag-on account starts instead of erroring).

- [ ] **Step 3: Register the routes**

In `internal/server/server.go`, in `Handler()` (near the other login routes):

```go
	mux.HandleFunc("GET /api/login/proxy-pool", s.handleGetLoginProxies)
	mux.HandleFunc("POST /api/login/proxy-pool", s.handleSetLoginProxies)
```

- [ ] **Step 4: Add the handlers and validator**

Add `"net/url"` and `"strings"` to the imports in `internal/server/server.go`, then add:

```go
// validateProxyURL checks a proxy entry parses and uses a supported scheme.
func validateProxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid proxy url %q: %v", raw, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return fmt.Errorf("proxy %q: scheme must be http, https, or socks5", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("proxy %q: missing host", raw)
	}
	return nil
}

func (s *Server) handleGetLoginProxies(w http.ResponseWriter, r *http.Request) {
	proxies, err := s.store.GetLoginProxies()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if proxies == nil {
		proxies = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxies": proxies, "count": len(proxies)})
}

func (s *Server) handleSetLoginProxies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proxies []string `json:"proxies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	cleaned := make([]string, 0, len(req.Proxies))
	for _, p := range req.Proxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := validateProxyURL(p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cleaned = append(cleaned, p)
	}
	if err := s.store.SetLoginProxies(cleaned); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [ ] **Step 5: Thread use_proxy_pool through the bulk handler**

In `handleBulkLogin`, extend the request struct and set the flag per account. Change from:

```go
	var req struct {
		Provider string            `json:"provider"`
		Accounts []auth.GoogleCred `json:"accounts"`
	}
```

to:

```go
	var req struct {
		Provider     string            `json:"provider"`
		Accounts     []auth.GoogleCred `json:"accounts"`
		UseProxyPool bool              `json:"use_proxy_pool"`
	}
```

Then, inside the `for i := range req.Accounts` loop, set the flag before copying the cred. Change from:

```go
	for i := range req.Accounts {
		cred := req.Accounts[i]
```

to:

```go
	for i := range req.Accounts {
		req.Accounts[i].UseProxyPool = req.UseProxyPool
		cred := req.Accounts[i]
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/server/ -v`
Expected: PASS (new tests plus existing bulk/callback tests).

- [ ] **Step 7: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): login proxy-pool endpoints and bulk toggle"
```

---

### Task 5: Frontend API, types, and hooks

**Files:**
- Modify: `web/src/lib/types.ts`
- Modify: `web/src/lib/api.ts`
- Create: `web/src/hooks/useLoginProxies.ts`
- Test: `web/src/lib/api.test.ts`

**Interfaces:**
- Produces:
  - `LoginProxyPool` type `{ proxies: string[]; count: number }`.
  - `bulkLogin(creds, provider?, useProxyPool?)` — adds `use_proxy_pool` to the body.
  - `getLoginProxies(): Promise<LoginProxyPool>`, `saveLoginProxies(proxies: string[]): Promise<void>`.
  - `useLoginProxies()`, `useSaveLoginProxies()` hooks.

- [ ] **Step 1: Write the failing tests**

First extend the import at the top of `web/src/lib/api.test.ts` from:

```ts
import { listAccounts, startManualLogin, refreshAccount, deleteAccount, bulkLogin } from "./api";
```

to:

```ts
import { listAccounts, startManualLogin, refreshAccount, deleteAccount, bulkLogin, getLoginProxies, saveLoginProxies } from "./api";
```

Then add these tests (follow the file's existing fetch-stub style; `vi`, `test`, `expect` are already imported):

```ts
test("bulkLogin sends use_proxy_pool in the body", async () => {
  const fetchMock = vi.fn(async () => new Response(
    JSON.stringify({ started: [], errors: [] }),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);
  await bulkLogin([{ email: "a@x.com", password: "pw" }], "google", true);
  const body = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(body.use_proxy_pool).toBe(true);
});

test("getLoginProxies maps the pool response", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(
    JSON.stringify({ proxies: ["http://a:8080"], count: 1 }),
    { status: 200, headers: { "content-type": "application/json" } },
  )));
  const pool = await getLoginProxies();
  expect(pool).toEqual({ proxies: ["http://a:8080"], count: 1 });
});

test("saveLoginProxies posts the proxies array", async () => {
  const fetchMock = vi.fn(async () => new Response(
    JSON.stringify({ status: "ok" }),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);
  await saveLoginProxies(["http://a:8080", "socks5://b:1080"]);
  const [url, init] = fetchMock.mock.calls[0];
  expect(url).toBe("/api/login/proxy-pool");
  expect((init as RequestInit).method).toBe("POST");
  expect(JSON.parse((init as RequestInit).body as string).proxies).toEqual(["http://a:8080", "socks5://b:1080"]);
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/lib/api.test.ts`
Expected: FAIL — `getLoginProxies`/`saveLoginProxies` not exported; `bulkLogin` ignores the third arg.

- [ ] **Step 3: Add the type**

In `web/src/lib/types.ts`, append:

```ts
export interface LoginProxyPool {
  proxies: string[];
  count: number;
}
```

- [ ] **Step 4: Update the API layer**

In `web/src/lib/api.ts`, add `LoginProxyPool` to the type import from `./types`, then change `bulkLogin` from:

```ts
export async function bulkLogin(
  creds: { email: string; password: string }[],
  provider: Provider = "google",
): Promise<BulkResult> {
  return (await req("/api/login/bulk", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ accounts: creds, provider }),
  })) as BulkResult;
}
```

to:

```ts
export async function bulkLogin(
  creds: { email: string; password: string }[],
  provider: Provider = "google",
  useProxyPool = false,
): Promise<BulkResult> {
  return (await req("/api/login/bulk", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ accounts: creds, provider, use_proxy_pool: useProxyPool }),
  })) as BulkResult;
}

export async function getLoginProxies(): Promise<LoginProxyPool> {
  const r = (await req("/api/login/proxy-pool")) as { proxies: string[]; count: number };
  return { proxies: r.proxies ?? [], count: r.count ?? 0 };
}

export async function saveLoginProxies(proxies: string[]): Promise<void> {
  await req("/api/login/proxy-pool", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ proxies }),
  });
}
```

- [ ] **Step 5: Create the hooks**

Create `web/src/hooks/useLoginProxies.ts`:

```ts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getLoginProxies, saveLoginProxies } from "../lib/api";

export function useLoginProxies() {
  return useQuery({ queryKey: ["login-proxies"], queryFn: getLoginProxies });
}

export function useSaveLoginProxies() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: saveLoginProxies,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["login-proxies"] }),
  });
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/lib/api.test.ts`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/lib/types.ts web/src/lib/api.ts web/src/hooks/useLoginProxies.ts web/src/lib/api.test.ts
git commit -m "feat(web): proxy-pool API client, types, and hooks"
```

---

### Task 6: Frontend — pool editor panel

**Files:**
- Create: `web/src/components/LoginProxyPanel.tsx`
- Create: `web/src/components/LoginProxyPanel.test.tsx`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `useLoginProxies`, `useSaveLoginProxies` (Task 5).
- Produces: `<LoginProxyPanel />` component.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/LoginProxyPanel.test.tsx`:

```tsx
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { LoginProxyPanel } from "./LoginProxyPanel";

afterEach(() => vi.restoreAllMocks());

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
}

function renderWithClient(ui: ReactNode) {
  return render(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);
}

test("loads the pool and saves edited proxies as an array", async () => {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (url === "/api/login/proxy-pool" && init?.method === "POST") return json({ status: "ok" });
    if (url === "/api/login/proxy-pool") return json({ proxies: ["http://a:8080"], count: 1 });
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);

  renderWithClient(<LoginProxyPanel />);
  await waitFor(() => expect(screen.getByDisplayValue("http://a:8080")).toBeInTheDocument());

  const box = screen.getByLabelText(/proxy pool/i);
  await userEvent.clear(box);
  await userEvent.type(box, "http://b:8080\nsocks5://c:1080");
  await userEvent.click(screen.getByRole("button", { name: /save pool/i }));

  await waitFor(() => {
    const post = fetchMock.mock.calls.find(
      (c) => c[0] === "/api/login/proxy-pool" && (c[1] as RequestInit)?.method === "POST",
    );
    expect(post).toBeTruthy();
    expect(JSON.parse((post![1] as RequestInit).body as string).proxies).toEqual([
      "http://b:8080",
      "socks5://c:1080",
    ]);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/components/LoginProxyPanel.test.tsx`
Expected: FAIL — cannot resolve `./LoginProxyPanel`.

- [ ] **Step 3: Create the panel**

Create `web/src/components/LoginProxyPanel.tsx`:

```tsx
import { useEffect, useState } from "react";
import { useLoginProxies, useSaveLoginProxies } from "../hooks/useLoginProxies";

export function LoginProxyPanel() {
  const pool = useLoginProxies();
  const save = useSaveLoginProxies();
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (pool.data) setText(pool.data.proxies.join("\n"));
  }, [pool.data]);

  if (pool.isLoading) {
    return <div className="h-24 animate-pulse rounded-xl border border-border bg-panel" />;
  }

  const lines = text.split("\n").map((l) => l.trim()).filter(Boolean);

  return (
    <section className="rounded-xl border border-border bg-surface p-4">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold">Add-account proxy pool</h2>
        <span className="text-xs text-muted">{pool.data?.count ?? 0} proxies</span>
      </div>
      <p className="mb-2 text-xs text-muted">
        One proxy URL per line (<code className="text-ink">http://user:pass@host:port</code>, also{" "}
        <code className="text-ink">https://</code> / <code className="text-ink">socks5://</code>). Used only when
        adding accounts with the pool toggle on — routes AutoGLM API calls around a flagged IP (error 630014).
      </p>
      <textarea
        aria-label="Proxy pool"
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={5}
        placeholder={"http://user:pass@1.2.3.4:8080\nsocks5://5.6.7.8:1080"}
        className="w-full rounded-lg border border-border bg-panel p-3 font-mono text-xs text-ink outline-none focus-visible:border-brand"
      />
      {error && <p className="mt-2 text-sm text-err">{error}</p>}
      <div className="mt-3">
        <button
          type="button"
          onClick={() => {
            setError(null);
            save.mutate(lines, {
              onError: (e) => setError(e instanceof Error ? e.message : "save failed"),
            });
          }}
          disabled={save.isPending}
          className="rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50"
        >
          {save.isPending ? "Saving…" : "Save pool"}
        </button>
      </div>
    </section>
  );
}
```

- [ ] **Step 4: Mount it in the dashboard**

In `web/src/App.tsx`, add the import after the `GatewayPanel` import:

```tsx
import { LoginProxyPanel } from "./components/LoginProxyPanel";
```

Then change:

```tsx
      <div className="mt-6">
        <GatewayPanel />
      </div>
```

to:

```tsx
      <div className="mt-6 space-y-6">
        <GatewayPanel />
        <LoginProxyPanel />
      </div>
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd web && npx vitest run src/components/LoginProxyPanel.test.tsx`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/LoginProxyPanel.tsx web/src/components/LoginProxyPanel.test.tsx web/src/App.tsx
git commit -m "feat(web): add-account proxy pool editor panel"
```

---

### Task 7: Frontend — Bulk login proxy toggle

**Files:**
- Modify: `web/src/components/BulkLogin.tsx`
- Test: `web/src/components/BulkLogin.test.tsx`

**Interfaces:**
- Consumes: `bulkLogin(creds, provider, useProxyPool)` and `getLoginProxies` (Task 5).
- Produces: a per-batch "Route AutoGLM API through proxy pool" checkbox, disabled when the pool is empty.

Note: `BulkLogin` is rendered in tests **without** a `QueryClientProvider`, so it must read the pool count with a plain `getLoginProxies()` call in `useEffect` (not the react-query hook) to avoid a "No QueryClient set" throw.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/components/BulkLogin.test.tsx`:

```tsx
test("proxy-pool toggle is enabled and sends use_proxy_pool when a pool exists", async () => {
  const fetchMock = vi.fn(async (url: string) => {
    if (url === "/api/login/proxy-pool") return json({ proxies: ["http://p:8080"], count: 1 });
    if (url === "/api/login/bulk") return json({ started: [], errors: [] });
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  const cb = await screen.findByRole("checkbox", { name: /proxy pool/i });
  expect(cb).toBeEnabled();
  await userEvent.click(cb);
  await userEvent.type(screen.getByLabelText(/credentials/i), "a@x.com:pw");
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.anything()));
  const call = fetchMock.mock.calls.find((c) => c[0] === "/api/login/bulk")!;
  expect(JSON.parse((call[1] as RequestInit).body as string).use_proxy_pool).toBe(true);
});

test("proxy-pool toggle is disabled when no proxies are configured", async () => {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (url === "/api/login/proxy-pool") return json({ proxies: [], count: 0 });
    return json({});
  }));
  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  const cb = await screen.findByRole("checkbox", { name: /proxy pool/i });
  expect(cb).toBeDisabled();
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/BulkLogin.test.tsx`
Expected: FAIL — no checkbox named /proxy pool/i.

- [ ] **Step 3: Add pool-count state and the toggle**

In `web/src/components/BulkLogin.tsx`:

Change the imports line from:

```tsx
import { bulkLogin, loginStatus } from "../lib/api";
```

to:

```tsx
import { bulkLogin, getLoginProxies, loginStatus } from "../lib/api";
```

Add state and a load effect near the other `useState` declarations in `BulkLogin`:

```tsx
  const [useProxyPool, setUseProxyPool] = useState(false);
  const [poolCount, setPoolCount] = useState(0);

  useEffect(() => {
    getLoginProxies()
      .then((p) => setPoolCount(p.count))
      .catch(() => setPoolCount(0));
  }, []);
```

Update the `bulkLogin` call in `submit()` from:

```tsx
      const res = await bulkLogin(creds, provider);
```

to:

```tsx
      const res = await bulkLogin(creds, provider, useProxyPool && poolCount > 0);
```

Add the checkbox immediately after the provider `</fieldset>`:

```tsx
        <label className="mt-3 flex items-center gap-2 text-sm text-muted">
          <input
            type="checkbox"
            checked={useProxyPool}
            disabled={poolCount === 0}
            onChange={(e) => setUseProxyPool(e.target.checked)}
          />
          Route AutoGLM API through proxy pool
          <span className="text-xs">
            {poolCount === 0 ? "(no proxies configured)" : `(${poolCount} ${poolCount === 1 ? "proxy" : "proxies"})`}
          </span>
        </label>
```

(`useEffect` and `useState` are already imported in this file.)

- [ ] **Step 4: Fix the one existing test that asserts an exact fetch count**

`BulkLogin` now fetches `/api/login/proxy-pool` on mount, so the existing test
`"shows a server-rejected account as failed without polling it"` — which asserts
`expect(fetchMock).toHaveBeenCalledTimes(1)` — would now see 2 calls. Its real
intent is "a row that never started is not polled". Change that assertion from:

```tsx
  // Only the bulk POST is called; a row that never started is not polled.
  expect(fetchMock).toHaveBeenCalledTimes(1);
```

to:

```tsx
  // A row that never started is not polled (no status?state= fetch happened).
  expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("state="))).toBe(false);
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/components/BulkLogin.test.tsx`
Expected: PASS (new toggle tests plus all existing BulkLogin tests — the pre-existing tests' catch-all `json({})` response makes `getLoginProxies` resolve to count 0, which the code guards).

- [ ] **Step 6: Full test sweep**

Run: `go test ./internal/... && cd web && npx vitest run`
Expected: all Go and frontend tests PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/components/BulkLogin.tsx web/src/components/BulkLogin.test.tsx
git commit -m "feat(web): per-batch proxy-pool toggle in bulk login"
```

---

## Verification

After Task 7, verify the feature end-to-end (per the `verify` skill): build the app, add one or more proxy URLs in the "Add-account proxy pool" panel, open Bulk login, confirm the toggle enables, run a bulk login with the toggle on, and confirm the login terminal shows the `attempt N/M via proxy …` lines and that a 630014 now fails over instead of failing the account.

Build check: `go build ./... && cd web && npm run build`.
