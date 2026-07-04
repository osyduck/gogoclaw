# Second Login Provider (Google via chat.z.ai) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a second OAuth provider (`zai` — Google login brokered through chat.z.ai) alongside the existing direct-Google provider, selectable per bulk/manual login run.

**Architecture:** Introduce an `api.Provider` value (`google`|`zai`) threaded from the UI toggle → `/api/login/{start,bulk}` → `AuthEngine.StartLogin` → `api.OAuthURL`/`OAuthLogin` (per-provider endpoint paths + `navigate_uri`) and → the sidecar driver (which adds a chat.z.ai pre-step and post-step for `zai`). One callback handler serves both `/auth/callback-google` and `/auth/callback-zai` because `state → session → provider`.

**Tech Stack:** Go 1.25 (net/http, modernc.org/sqlite), Python 3 sidecar (aiohttp + cloakbrowser/Playwright), Vite + React 19 + TS.

## Global Constraints

- Module `gogoclaw`; Go floor **1.25.0**.
- Git identity for commits: `user.name=osyduck`, `user.email=osyduck@icloud.com` (use `git -c user.name='osyduck' -c user.email='osyduck@icloud.com' commit`).
- Google credentials are one-time/in-memory — never persisted or logged.
- Callback path pattern is fixed: `http://localhost:18432/auth/callback-<provider>`.
- Provider strings are validated against the known set; never build an endpoint path from unvalidated input.
- Response envelope, signing (`x-auth-sign`), device identity, and refresh are unchanged and provider-agnostic.

---

### Task 1: `api` — Provider abstraction

**Files:**
- Modify: `internal/api/sign.go` (remove `NavigateURI` const)
- Modify: `internal/api/client.go` (Provider type, `NavigateURIFor`, `OAuthURL`/`OAuthLogin`, Google wrappers)
- Test: `internal/api/client_test.go`

**Interfaces:**
- Produces:
  - `type Provider string`; `const ProviderGoogle Provider = "google"`, `ProviderZai Provider = "zai"`.
  - `func ParseProvider(s string) (Provider, error)` — `""` and `"google"` → `ProviderGoogle`; `"zai"` → `ProviderZai`; anything else → error.
  - `func NavigateURIFor(p Provider) string` → `"http://localhost:18432/auth/callback-" + string(p)`.
  - `func (c *Client) OAuthURL(ctx context.Context, p Provider, deviceID string) (string, string, error)`.
  - `func (c *Client) OAuthLogin(ctx context.Context, p Provider, deviceID, code, state string) (LoginResult, error)`.
  - `GoogleOAuthURL`/`GoogleOAuthLogin` keep their existing signatures, delegating with `ProviderGoogle`.
- Consumes: `postSigned`, `LoginResult`, `SourceID` (existing).

- [ ] **Step 1: Write failing tests**

Add to `internal/api/client_test.go` (and update the two existing `NavigateURI` references in `TestGoogleOAuthURL`/`TestGoogleOAuthLogin` to `NavigateURIFor(ProviderGoogle)`):

```go
func TestOAuthURL_ZaiUsesZaiPathAndCallback(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/overseasv1/zai-oauth-url" {
			t.Errorf("path = %s", path)
		}
		if body["navigate_uri"] != "http://localhost:18432/auth/callback-zai" {
			t.Errorf("navigate_uri = %v", body["navigate_uri"])
		}
		return map[string]any{"oauth_url": "https://chat.z.ai/x", "state": "st-z"}
	})
	defer done()
	url, state, err := c.OAuthURL(context.Background(), ProviderZai, "dev1")
	if err != nil || url != "https://chat.z.ai/x" || state != "st-z" {
		t.Fatalf("got %q/%q err=%v", url, state, err)
	}
}

func TestOAuthLogin_ZaiUsesZaiPath(t *testing.T) {
	c, done := newTestClient(t, func(path string, body map[string]any, auth string) any {
		if path != "/userapi/overseasv1/zai-oauth-login" {
			t.Errorf("path = %s", path)
		}
		return map[string]any{"access_token": "Bearer a", "refresh_token": "Bearer r", "user_id": "u"}
	})
	defer done()
	res, err := c.OAuthLogin(context.Background(), ProviderZai, "dev1", "code-1", "st-z")
	if err != nil || res.AccessToken != "Bearer a" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestParseProvider(t *testing.T) {
	for in, want := range map[string]Provider{"": ProviderGoogle, "google": ProviderGoogle, "zai": ProviderZai} {
		if got, err := ParseProvider(in); err != nil || got != want {
			t.Errorf("ParseProvider(%q) = %q,%v want %q", in, got, err, want)
		}
	}
	if _, err := ParseProvider("evil/../path"); err == nil {
		t.Error("expected error for unknown provider")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ -run 'OAuth|ParseProvider' -v`
Expected: FAIL (undefined: ProviderZai / OAuthURL / ParseProvider / NavigateURIFor).

- [ ] **Step 3: Implement**

In `internal/api/sign.go`, delete the `NavigateURI` const line (keep `BaseURL`, `SourceID`).

In `internal/api/client.go`, add near the top (after imports):

```go
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
```

Replace `GoogleOAuthURL`/`GoogleOAuthLogin` with generalized methods + wrappers:

```go
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
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/api/ -v`
Expected: PASS (all, including existing Google tests via wrappers).

- [ ] **Step 5: Commit**

```bash
git add internal/api/
git -c user.name='osyduck' -c user.email='osyduck@icloud.com' commit -m "feat(api): provider abstraction for google + zai oauth endpoints"
```

---

### Task 2: `auth` — provider-aware sessions, callback, and driver interface

**Files:**
- Modify: `internal/auth/auth.go` (`Session.provider`, `StartLogin` signature, `HandleCallback`, `LoginDriver` interface, `ManualDriver.Drive`)
- Modify: `internal/auth/autodriver.go` (`Drive` signature + `provider` in payload)
- Test: `internal/auth/auth_test.go` (update `failDriver`, add a zai callback test)

**Interfaces:**
- Consumes: `api.Provider`, `api.ProviderGoogle`, `api.ProviderZai`, `api.OAuthURL`, `api.OAuthLogin` (Task 1).
- Produces:
  - `type LoginDriver interface { Drive(ctx context.Context, provider api.Provider, oauthURL string, cred *GoogleCred) error }`.
  - `func (e *AuthEngine) StartLogin(ctx context.Context, driver LoginDriver, provider api.Provider, cred *GoogleCred) (string, string, error)`.
  - `AutoDriver`/`ManualDriver` `Drive` match the new interface.

- [ ] **Step 1: Update the test driver + write a failing zai test**

In `internal/auth/auth_test.go`, change `failDriver.Drive` to the new signature and update the existing `StartLogin(...)` call in `TestStartLogin_AutoFailureAttributesEmail` to pass a provider:

```go
func (f failDriver) Drive(context.Context, api.Provider, string, *GoogleCred) error { return f.err }
```
```go
// in TestStartLogin_AutoFailureAttributesEmail:
state, _, err := e.StartLogin(context.Background(), failDriver{err: errors.New("stealth login failed: blocked")}, api.ProviderZai, cred)
```

Update the three `ManualDriver{}` `StartLogin` calls in that file to include the provider argument, e.g. `e.StartLogin(context.Background(), ManualDriver{}, api.ProviderGoogle, nil)`.

Add a new test that a zai session's callback hits the zai login endpoint:

```go
func TestHandleCallback_ZaiUsesZaiLoginEndpoint(t *testing.T) {
	var loginPath string
	e, st := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/userapi/overseasv1/zai-oauth-url":
			data = map[string]any{"oauth_url": "https://chat.z.ai/x", "state": "st-z"}
		case "/userapi/overseasv1/zai-oauth-login":
			loginPath = r.URL.Path
			data = map[string]any{"access_token": jwtA, "refresh_token": jwtA, "user_id": "hexid"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	if _, _, err := e.StartLogin(context.Background(), ManualDriver{}, api.ProviderZai, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.HandleCallback(context.Background(), "code-z", "st-z"); err != nil {
		t.Fatal(err)
	}
	if loginPath != "/userapi/overseasv1/zai-oauth-login" {
		t.Errorf("login path = %q, want zai endpoint", loginPath)
	}
	if _, err := st.Get("evmsnipe@gmail.com"); err != nil {
		t.Errorf("account not persisted: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/auth/ -run 'Zai|AutoFailure' -v`
Expected: FAIL to compile (Drive signature mismatch / StartLogin arity).

- [ ] **Step 3: Implement**

In `internal/auth/auth.go`:

```go
type LoginDriver interface {
	Drive(ctx context.Context, provider api.Provider, oauthURL string, cred *GoogleCred) error
}

func (ManualDriver) Drive(ctx context.Context, provider api.Provider, oauthURL string, cred *GoogleCred) error {
	return nil
}
```

Add `provider api.Provider` field to `Session` (unexported, after `identity`). Change `StartLogin`:

```go
func (e *AuthEngine) StartLogin(ctx context.Context, driver LoginDriver, provider api.Provider, cred *GoogleCred) (string, string, error) {
	id, err := identity.New()
	if err != nil {
		return "", "", fmt.Errorf("generate identity: %w", err)
	}
	oauthURL, state, err := e.api.OAuthURL(ctx, provider, id.DeviceID)
	if err != nil {
		return "", "", fmt.Errorf("oauth url: %w", err)
	}
	sess := &Session{State: state, Status: "pending", CreatedAt: time.Now(), identity: id, provider: provider}
	if cred != nil {
		sess.Email = cred.Email
	}
	e.mu.Lock()
	e.pending[state] = sess
	e.mu.Unlock()

	go func() {
		if err := driver.Drive(context.Background(), provider, oauthURL, cred); err != nil {
			e.fail(state, fmt.Errorf("driver: %w", err))
		}
	}()
	return state, oauthURL, nil
}
```

In `HandleCallback`, capture the provider under the lock and use it for the login call. After the existing `e.mu.Unlock()` that reads `status, sessErr`, also read `sess.provider` (add `provider := sess.provider` inside the `if ok` block, declared alongside). Then replace the login call:

```go
	res, err := e.api.OAuthLogin(ctx, provider, sess.identity.DeviceID, code, state)
```

In `internal/auth/autodriver.go`, change `Drive` to accept the provider and send it:

```go
func (d *AutoDriver) Drive(ctx context.Context, provider api.Provider, oauthURL string, cred *GoogleCred) error {
	if cred == nil {
		return fmt.Errorf("auto login requires Google credentials")
	}
	// ... semaphore unchanged ...
	payload := map[string]string{
		"oauth_url": oauthURL, "email": cred.Email, "password": cred.Password,
		"proxy": cred.Proxy, "provider": string(provider),
	}
	// ... rest unchanged ...
}
```

Add `"gogoclaw/internal/api"` to `autodriver.go` imports.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/
git -c user.name='osyduck' -c user.email='osyduck@icloud.com' commit -m "feat(auth): thread provider through sessions, callback, and driver"
```

---

### Task 3: `server` — routes and provider passthrough

**Files:**
- Modify: `internal/server/server.go` (route, `handleLoginStart`, `handleBulkLogin`)
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `api.ParseProvider`, `AuthEngine.StartLogin(…, provider, …)` (Tasks 1–2).
- Produces: `GET /auth/callback-zai` route; `provider` field accepted on `/api/login/start` and `/api/login/bulk`.

- [ ] **Step 1: Write failing tests**

Add to `internal/server/server_test.go` (uses the existing `newServer` helper; assumes its autoglm handler returns a generic `{code:0,...,data:{oauth_url,state}}` — if the helper is provider-specific, have it respond to any `*-oauth-url` path):

```go
func TestCallbackZaiRouteExists(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": map[string]any{}})
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/callback-zai?code=c&state=nope", nil))
	// Unknown state → 400 (not 404): the route is wired to the callback handler.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("callback-zai code = %d, want 400 (routed)", rec.Code)
	}
}

func TestLoginStart_RejectsUnknownProvider(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": map[string]any{}})
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"manual","provider":"evil"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown provider code = %d, want 400", rec.Code)
	}
}
```

Ensure `strings` is imported in the test file.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/ -run 'Zai|UnknownProvider' -v`
Expected: FAIL (callback-zai 404; provider not validated).

- [ ] **Step 3: Implement**

In `internal/server/server.go` `Handler()`, add after the callback-google route:

```go
	mux.HandleFunc("GET /auth/callback-zai", s.handleCallback)
```

In `handleLoginStart`, extend the request struct and validate/pass the provider:

```go
	var req struct {
		Mode     string           `json:"mode"`
		Provider string           `json:"provider"`
		Cred     *auth.GoogleCred `json:"cred"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	provider, err := api.ParseProvider(req.Provider)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
```

Then pass `provider` in both `StartLogin` calls in that function:
`s.engine.StartLogin(r.Context(), s.autoLogin.Driver(), provider, req.Cred)` (auto branch) and
`s.engine.StartLogin(r.Context(), auth.ManualDriver{}, provider, nil)` (manual branch).

In `handleBulkLogin`, extend the request struct and validate once, before dispatch:

```go
	var req struct {
		Provider string            `json:"provider"`
		Accounts []auth.GoogleCred `json:"accounts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Accounts) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected non-empty accounts array"})
		return
	}
	provider, err := api.ParseProvider(req.Provider)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
```

And pass `provider` in the goroutine's `StartLogin`:
`s.engine.StartLogin(r.Context(), driver, provider, &cred)`.

Add `"gogoclaw/internal/api"` to `server.go` imports.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/server/ ./cmd/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git -c user.name='osyduck' -c user.email='osyduck@icloud.com' commit -m "feat(server): callback-zai route + validated provider on start/bulk"
```

---

### Task 4: `sidecar` — provider-aware stealth driver

**Files:**
- Modify: `sidecar/driver.py` (provider param, shared Google helper, zai pre/post steps, locale-robust consent w/ short timeouts)
- Modify: `sidecar/server.py` (forward `provider` to `drive_fn`)
- Test: `sidecar/test_sidecar.py`

**Interfaces:**
- Consumes: `provider` string in the `/drive` payload (Task 2 sends `"provider"`).
- Produces: `drive(oauth_url, email, password, proxy=None, provider="google", launcher=_default_launcher, headless=True)`.

- [ ] **Step 1: Extend the fake + write failing tests**

In `sidecar/test_sidecar.py`, give `FakeLocator` a `check()` and make `click`/`fill` accept kwargs (real code passes `timeout=`):

```python
class FakeLocator:
    def __init__(self, calls, sel):
        self.calls = calls
        self.sel = sel

    def fill(self, value, **kw):
        self.calls.append(("fill", self.sel, value))

    def click(self, **kw):
        self.calls.append(("click", self.sel))

    def check(self, **kw):
        self.calls.append(("check", self.sel))
```

Add a zai-flow test:

```python
def test_drive_zai_clicks_google_then_authorizes():
    calls = []
    browser = FakeBrowser(calls)
    result = drive("https://chat.z.ai/x", "a@x.com", "pw", provider="zai",
                   launcher=lambda **kw: browser)
    assert result == {"ok": True}
    sels = [c[1] for c in calls if c[0] == "click"]
    # pre-step: continue with Google on chat.z.ai
    assert any("Continue with Google" in s for s in sels)
    # Google login still happens
    assert ("fill", "#identifierId", "a@x.com") in calls
    # post-step: chat.z.ai ToS checkbox is ticked and its Continue is clicked
    assert any(c[0] == "check" for c in calls)
    assert any(s == "button:has-text('Continue')" for s in sels)
    assert any(c[0] == "wait_for_url" for c in calls)
```

Update the two endpoint-test lambdas that hardcode the signature to accept `provider`:
`make_app(drive_fn=lambda oauth_url, email, password, proxy=None, provider="google": {"ok": True})` in `test_drive_endpoint_ok`.

- [ ] **Step 2: Run to verify failure**

Run: `cd sidecar && python -m pytest test_sidecar.py -k zai -v` (or from repo root: `python -m pytest sidecar/test_sidecar.py -k zai -v`)
Expected: FAIL (`drive()` ignores provider; no checkbox/Google-button clicks).

- [ ] **Step 3: Implement**

Rewrite `sidecar/driver.py`:

```python
"""Drives OAuth consent in a stealth CloakBrowser to the callback redirect.

Two providers:
- "google": Google's own consent page (email/password + consent).
- "zai": chat.z.ai brokers the Google login — an extra "Continue with Google"
  pre-step and a chat.z.ai authorize (ToS checkbox + Continue) post-step wrap
  the same Google steps.

Stateless: it knows nothing about tokens/state. The browser's redirect to
http://localhost:18432/** is captured by GogoClaw's own callback handler; this
module only reports whether the browser reached that point.
"""

CALLBACK_URL_GLOB = "http://localhost:18432/**"
BEST_EFFORT_MS = 2500

# Approve/continue buttons vary by the account's Google locale, plus a
# Workspace/Education "I understand" speedbump. Best-effort, never fatal.
CONSENT_SELECTORS = [
    "#submit_approve_access",
    "button:has-text('I understand')",
    "button:has-text('Continue')",
    "button:has-text('Allow')",
    "button:has-text('Lanjutkan')",   # id
    "button:has-text('Izinkan')",     # id
    "button:has-text('Continuar')",   # es/pt
    "button:has-text('Weiter')",      # de
    "button:has-text('Autoriser')",   # fr
]


def _default_launcher(*, headless, humanize, proxy):
    from cloakbrowser import launch

    return launch(headless=headless, humanize=humanize, proxy=proxy)


def _best_effort(fn):
    try:
        fn()
    except Exception:
        pass


def _google_login(page, email, password):
    page.locator("#identifierId").fill(email)
    page.locator("#identifierNext").click()
    page.locator('input[name="Passwd"]').fill(password)
    page.locator("#passwordNext").click()
    for sel in CONSENT_SELECTORS:
        _best_effort(lambda sel=sel: page.locator(sel).click(timeout=BEST_EFFORT_MS))


def drive(oauth_url, email, password, proxy=None, provider="google",
          launcher=_default_launcher, headless=True):
    """Drive the browser to the OAuth callback. Returns {"ok": True} or
    {"ok": False, "reason": "..."}."""
    browser = None
    try:
        browser = launcher(headless=headless, humanize=True, proxy=proxy)
        page = browser.new_page()
        page.goto(oauth_url)
        if provider == "zai":
            # chat.z.ai login page → hand off to Google.
            page.locator("button:has-text('Continue with Google')").click(timeout=BEST_EFFORT_MS * 4)
        _google_login(page, email, password)
        if provider == "zai":
            # chat.z.ai authorize: Continue is disabled until ToS is ticked.
            _best_effort(lambda: page.locator("input[type='checkbox']").check(timeout=BEST_EFFORT_MS))
            _best_effort(lambda: page.locator("button:has-text('Continue')").click(timeout=BEST_EFFORT_MS * 4))
        page.wait_for_url(CALLBACK_URL_GLOB)
        return {"ok": True}
    except Exception as exc:  # wrong password, 2FA, captcha, timeout, …
        return {"ok": False, "reason": str(exc)}
    finally:
        if browser is not None:
            try:
                browser.close()
            except Exception:
                pass
```

In `sidecar/server.py`, forward the provider (keyword, default google):

```python
        result = await loop.run_in_executor(
            None,
            lambda: drive_fn(body["oauth_url"], body["email"], body["password"],
                             body.get("proxy"), provider=body.get("provider", "google")),
        )
```

- [ ] **Step 4: Run to verify pass**

Run: `python -m pytest sidecar/test_sidecar.py -v`
Expected: PASS (all, including the existing Google `test_drive_success_fills_and_waits`).

- [ ] **Step 5: Commit**

```bash
git add sidecar/
git -c user.name='osyduck' -c user.email='osyduck@icloud.com' commit -m "feat(sidecar): provider-aware driver with chat.z.ai zai flow"
```

---

### Task 5: `web` — provider toggle in Bulk + Add-account

**Files:**
- Modify: `web/src/lib/types.ts` (`Provider` type)
- Modify: `web/src/lib/api.ts` (`bulkLogin`, `startManualLogin` accept provider)
- Modify: `web/src/components/BulkLogin.tsx` (toggle)
- Modify: `web/src/components/AddAccount.tsx` (toggle)
- Test: `web/src/lib/api.test.ts`, `web/src/components/BulkLogin.test.tsx`
- Rebuild: `web/dist` (embedded)

**Interfaces:**
- Consumes: `/api/login/{start,bulk}` accepting `provider` (Task 3).
- Produces: `type Provider = "google" | "zai"`; `bulkLogin(creds, provider)`, `startManualLogin(provider)`.

- [ ] **Step 1: Write failing tests**

In `web/src/lib/api.test.ts`, add:

```ts
test("bulkLogin sends the selected provider", async () => {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify({ started: [], errors: [] }), {
    status: 200, headers: { "content-type": "application/json" },
  }));
  vi.stubGlobal("fetch", fetchMock);
  await bulkLogin([{ email: "a@x.com", password: "pw" }], "zai");
  const body = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(body.provider).toBe("zai");
});
```

Import `bulkLogin` in that test file if not already imported.

In `web/src/components/BulkLogin.test.tsx`, add:

```ts
test("defaults to Direct Google and can switch to via chat.z.ai", async () => {
  const fetchMock = vi.fn(async (url: string) => new Response(
    JSON.stringify(url === "/api/login/bulk" ? { started: [], errors: [] } : {}),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);
  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  await userEvent.type(screen.getByLabelText(/credentials/i), "a@x.com:pw");
  await userEvent.click(screen.getByRole("radio", { name: /chat\.z\.ai/i }));
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.anything()));
  const body = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(body.provider).toBe("zai");
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && npx vitest run src/lib/api.test.ts src/components/BulkLogin.test.tsx`
Expected: FAIL (no provider in body; no radio).

- [ ] **Step 3: Implement**

In `web/src/lib/types.ts` add: `export type Provider = "google" | "zai";`

In `web/src/lib/api.ts`, update the two functions:

```ts
export async function startManualLogin(provider: Provider = "google"): Promise<LoginStart> {
  const r = (await req("/api/login/start", {
    method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({ mode: "manual", provider }),
  })) as { state: string; oauth_url: string };
  return { state: r.state, oauthUrl: r.oauth_url };
}

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

Import `Provider` (and keep existing imports) in `api.ts`: `import type { Account, LoginSession, LoginStart, Provider } from "./types";`

In `web/src/components/BulkLogin.tsx`, add provider state and a small radio toggle, and pass it to `bulkLogin`:

```tsx
import type { Provider } from "../lib/types";
// inside component:
const [provider, setProvider] = useState<Provider>("google");
// in submit(): const res = await bulkLogin(creds, provider);
```

Render the toggle above the summary/rows (after the credentials hint `<p>`):

```tsx
<fieldset className="mt-3 flex gap-4 text-sm">
  <legend className="sr-only">Login provider</legend>
  {([["google", "Direct Google"], ["zai", "via chat.z.ai"]] as [Provider, string][]).map(([val, label]) => (
    <label key={val} className="inline-flex items-center gap-2 text-muted">
      <input type="radio" name="provider" value={val}
        checked={provider === val} onChange={() => setProvider(val)} />
      {label}
    </label>
  ))}
</fieldset>
```

In `web/src/components/AddAccount.tsx`, add the same `provider` state + toggle and call `startManualLogin(provider)` instead of `startManualLogin()`. Place the toggle next to the Add button:

```tsx
import type { Provider } from "../lib/types";
const [provider, setProvider] = useState<Provider>("google");
// start(): const { oauthUrl } = await startManualLogin(provider);
```
```tsx
<select aria-label="Login provider" value={provider}
  onChange={(e) => setProvider(e.target.value as Provider)}
  className="rounded-lg border border-border bg-panel px-2 py-2 text-sm text-ink">
  <option value="google">Direct Google</option>
  <option value="zai">via chat.z.ai</option>
</select>
```

- [ ] **Step 4: Run tests + typecheck to verify pass**

Run: `cd web && npx tsc --noEmit && npx vitest run`
Expected: PASS (all web tests).

- [ ] **Step 5: Rebuild the embedded bundle**

Run: `cd web && npm run build`
Then from repo root confirm the Go binary embeds it: `go build ./cmd/gogoclaw`
Expected: build succeeds; `web/dist/assets` has exactly the two freshly hashed files referenced by `web/dist/index.html`.

- [ ] **Step 6: Commit**

```bash
git add web/
git -c user.name='osyduck' -c user.email='osyduck@icloud.com' commit -m "feat(web): provider toggle (Direct Google / via chat.z.ai) in bulk + add"
```

---

### Task 6: Full-suite verification

- [ ] **Step 1: Go**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages `ok`.

- [ ] **Step 2: Python**

Run: `python -m pytest sidecar/test_sidecar.py -v`
Expected: all pass.

- [ ] **Step 3: Web**

Run: `cd web && npx tsc --noEmit && npx vitest run`
Expected: all pass.

- [ ] **Step 4: Live smoke (manual, optional)**

Run `./gogoclaw`, open the dashboard, choose "via chat.z.ai" in Bulk, submit the test account `jemuluof428@deciw.com:Asdasd123`, and confirm the row reaches **Signed in** (or shows a precise reason). The startup log should read `auto-login enabled via <python>`.

## Self-Review

- **Spec coverage:** api Provider (T1) · auth session/callback/driver (T2) · server routes+validation (T3) · sidecar zai driver w/ locale-robust consent + ToS checkbox (T4) · web toggle (T5) · verification (T6). All spec sections mapped.
- **Placeholders:** none — every code step is complete.
- **Type consistency:** `Provider`/`ProviderGoogle`/`ProviderZai`, `ParseProvider`, `NavigateURIFor`, `OAuthURL`/`OAuthLogin`, `StartLogin(…, provider, …)`, `LoginDriver.Drive(ctx, provider, url, cred)`, `drive(…, provider=…)`, and web `bulkLogin(creds, provider)`/`startManualLogin(provider)` are consistent across tasks.
