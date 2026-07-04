# GogoClaw — Plan 4: Stealth Auto-Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add stealth bulk auto-login: a Python CloakBrowser sidecar drives Google consent for pasted `email:password` credentials, GogoClaw's `AutoDriver` calls it, GogoClaw auto-spawns it on demand, and a Bulk-login UI submits many accounts at once — all converging on the existing `/auth/callback-google` capture.

**Architecture:** A stateless Python **sidecar** (aiohttp on `127.0.0.1:31500`) exposes `POST /drive {oauth_url,email,password,proxy?}`; it launches a CloakBrowser (stealth Chromium, humanized) and drives Google to the redirect, returning `{ok, reason?}` — it never touches tokens/state (the browser's redirect to `:18432` is captured by the existing callback). Go's `auth.AutoDriver` implements `LoginDriver` by POSTing to the sidecar. A Go `sidecar.Manager` spawns the sidecar subprocess on demand (health-checked, idempotent) the first time auto-login runs. The server's `/api/login/start` `mode:"auto"` and a new `/api/login/bulk` use it; the React Bulk-login modal submits `email:password` lines with per-account progress via the existing SSE stream.

**Tech Stack:** Go 1.25 (stdlib `net/http`, `os/exec`), Python 3.11 (`aiohttp`, `cloakbrowser`; tests use `pytest` + `pytest-aiohttp` with CloakBrowser mocked), React/Vite (existing).

## Global Constraints

- Sidecar binds loopback only: `127.0.0.1:31500`. GogoClaw stays on `127.0.0.1:18432`.
- Google credentials are one-time, in-memory only — never persisted (no DB column, no disk). They transit Go's `AutoDriver` → sidecar POST body and are discarded after the drive.
- The sidecar is **stateless**: it knows nothing about `state`/`code`/tokens. It drives the browser to the `http://localhost:18432/**` redirect and reports `{ok, reason?}`. Token capture stays in the existing `AuthEngine.HandleCallback`.
- The sidecar contract: `POST /drive {oauth_url, email, password, proxy?}` → `200 {ok:true}` on reaching the callback redirect, or `{ok:false, reason:"..."}` (wrong password / 2FA / captcha / timeout). `GET /health` → `200 {ok:true}`.
- GogoClaw **auto-spawns** the sidecar on demand (subprocess) the first time auto-login is triggered; reuse if already healthy; terminate on GogoClaw shutdown. No manual start.
- CloakBrowser must be imported **lazily** (only when actually driving) so the sidecar's tests run with a mocked launcher and do NOT require the real Chromium download.
- Bulk auto-login concurrency is bounded (max 3 concurrent drives) to avoid Google flagging.
- `proxy` flows through the whole contract (Go `AutoDriver` → sidecar → CloakBrowser) but the UI wiring for per-account proxies is deferred; pass `proxy` as optional/empty for now.
- Reuse Plan 2/3 verbatim: `auth.LoginDriver` (`Drive(ctx, oauthURL string, cred *GoogleCred) error`), `auth.GoogleCred{Email,Password}`, `auth.AuthEngine.StartLogin(ctx, driver, cred)`, the server's `handleLoginStart` (currently 501 for `mode:"auto"`), the SSE stream + `["accounts"]` invalidation, `AddAccount`'s pattern.

## Plan roadmap (this is Plan 4 of 4 — final)

1. Plan 1 — Go core ✅. 2. Plan 2 — Server + manual login ✅. 3. Plan 3 — Web dashboard ✅. 4. **Plan 4 — Stealth auto-login (this doc).**

---

### Task 1: Go AutoDriver (`internal/auth/autodriver.go`)

**Files:**
- Create: `internal/auth/autodriver.go`
- Test: `internal/auth/autodriver_test.go`

**Interfaces:**
- Consumes: `auth.LoginDriver`, `auth.GoogleCred` (from Plan 2).
- Produces: `auth.AutoDriver` implementing `LoginDriver`; `func NewAutoDriver(sidecarURL string) *AutoDriver`. `Drive` POSTs `{oauth_url,email,password,proxy}` to `<sidecarURL>/drive`; a `{ok:false}` (or transport error) is a `Drive` error.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/autodriver_test.go`:
```go
package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAutoDriver_PostsCredsAndSucceeds(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive" || r.Method != "POST" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	d := NewAutoDriver(srv.URL)
	err := d.Drive(context.Background(), "https://accounts.google.com/o", &GoogleCred{Email: "a@x.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["oauth_url"] != "https://accounts.google.com/o" || gotBody["email"] != "a@x.com" || gotBody["password"] != "pw" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestAutoDriver_FailureReasonBecomesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "reason": "wrong password"})
	}))
	defer srv.Close()
	err := NewAutoDriver(srv.URL).Drive(context.Background(), "u", &GoogleCred{Email: "a@x.com", Password: "bad"})
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("expected error containing reason, got %v", err)
	}
}

func TestAutoDriver_NilCredErrors(t *testing.T) {
	if err := NewAutoDriver("http://127.0.0.1:1").Drive(context.Background(), "u", nil); err == nil {
		t.Error("expected error for nil credentials")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/auth/ -run TestAutoDriver -v`
Expected: FAIL — `undefined: NewAutoDriver`.

- [ ] **Step 3: Write the implementation**

Create `internal/auth/autodriver.go`:
```go
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
```

- [ ] **Step 4: Add the `Proxy` field to `GoogleCred`**

The `GoogleCred` struct (in `internal/auth/auth.go`) currently has `Email, Password`. Add a `Proxy` field so the contract carries it end-to-end. Change the struct to:
```go
// GoogleCred is a one-time Google credential (used only by the Plan 4 auto driver).
type GoogleCred struct {
	Email    string
	Password string
	Proxy    string // optional residential proxy; empty = none
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS (all auth tests incl. the 3 new AutoDriver tests; the Plan 2/3 auth tests still pass — the added field is zero-valued).

- [ ] **Step 6: Commit**

```bash
git add internal/auth/autodriver.go internal/auth/autodriver_test.go internal/auth/auth.go
git commit -m "feat(auth): AutoDriver posts credentials to the CloakBrowser sidecar"
```

---

### Task 2: Python CloakBrowser sidecar (`sidecar/`)

**Files:**
- Create: `sidecar/__init__.py`, `sidecar/driver.py`, `sidecar/server.py`, `sidecar/__main__.py`
- Create: `sidecar/requirements.txt`, `sidecar/requirements-dev.txt`
- Test: `sidecar/test_sidecar.py`
- Modify: `.gitignore` (ignore Python caches)

**Interfaces:**
- Consumes: nothing (standalone).
- Produces:
  - `driver.drive(oauth_url, email, password, proxy=None, launcher=..., headless=True) -> dict` — drives the browser; returns `{"ok": True}` or `{"ok": False, "reason": str}`. `launcher` is injectable; the default lazily imports `cloakbrowser.launch`.
  - `server.make_app(drive_fn=drive) -> aiohttp.web.Application` with `POST /drive` and `GET /health`.
  - `python -m sidecar` runs the server on `127.0.0.1:31500`.

- [ ] **Step 1: Write the failing test**

Create `sidecar/test_sidecar.py`:
```python
import pytest
from sidecar.driver import drive
from sidecar.server import make_app


class FakeLocator:
    def __init__(self, calls, sel):
        self.calls = calls
        self.sel = sel

    def fill(self, value):
        self.calls.append(("fill", self.sel, value))

    def click(self):
        self.calls.append(("click", self.sel))


class FakePage:
    def __init__(self, calls, fail_on_wait=False):
        self.calls = calls
        self.fail_on_wait = fail_on_wait

    def goto(self, url):
        self.calls.append(("goto", url))

    def locator(self, sel):
        return FakeLocator(self.calls, sel)

    def wait_for_url(self, glob, **kw):
        if self.fail_on_wait:
            raise RuntimeError("navigation timeout")
        self.calls.append(("wait_for_url", glob))


class FakeBrowser:
    def __init__(self, calls, fail_on_wait=False):
        self.calls = calls
        self.fail_on_wait = fail_on_wait
        self.closed = False

    def new_page(self):
        return FakePage(self.calls, self.fail_on_wait)

    def close(self):
        self.closed = True


def test_drive_success_fills_and_waits():
    calls = []
    browser = FakeBrowser(calls)
    result = drive("https://g/o", "a@x.com", "pw", launcher=lambda **kw: browser)
    assert result == {"ok": True}
    # email + password were filled and the callback redirect was awaited
    assert ("fill", "#identifierId", "a@x.com") in calls
    assert ("fill", 'input[name="Passwd"]', "pw") in calls
    assert any(c[0] == "wait_for_url" for c in calls)
    assert browser.closed is True  # browser always closed


def test_drive_failure_returns_reason():
    browser = FakeBrowser([], fail_on_wait=True)
    result = drive("https://g/o", "a@x.com", "pw", launcher=lambda **kw: browser)
    assert result["ok"] is False
    assert "timeout" in result["reason"]
    assert browser.closed is True


async def test_drive_endpoint_ok(aiohttp_client):
    app = make_app(drive_fn=lambda oauth_url, email, password, proxy=None: {"ok": True})
    client = await aiohttp_client(app)
    resp = await client.post("/drive", json={"oauth_url": "u", "email": "a@x.com", "password": "pw"})
    assert resp.status == 200
    assert await resp.json() == {"ok": True}


async def test_drive_endpoint_missing_field(aiohttp_client):
    app = make_app(drive_fn=lambda **kw: {"ok": True})
    client = await aiohttp_client(app)
    resp = await client.post("/drive", json={"email": "a@x.com"})
    assert resp.status == 400


async def test_health(aiohttp_client):
    client = await aiohttp_client(make_app())
    resp = await client.get("/health")
    assert resp.status == 200
    assert (await resp.json())["ok"] is True
```

- [ ] **Step 2: Create the requirements + install dev deps**

Create `sidecar/requirements.txt`:
```
aiohttp>=3.9
cloakbrowser>=0.4.5
```
Create `sidecar/requirements-dev.txt`:
```
aiohttp>=3.9
pytest>=8
pytest-aiohttp>=1.0
```
Run:
```bash
cd /e/autoclaw && python -m pip install -r sidecar/requirements-dev.txt
```
Expected: installs aiohttp, pytest, pytest-aiohttp (NOT cloakbrowser — tests mock it).

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /e/autoclaw && python -m pytest sidecar/test_sidecar.py -q`
Expected: FAIL — `ModuleNotFoundError: No module named 'sidecar.driver'`.

- [ ] **Step 4: Write the implementation**

Create `sidecar/__init__.py` (empty file).

Create `sidecar/driver.py`:
```python
"""Drives Google OAuth consent in a stealth CloakBrowser to the callback redirect.

Stateless: it knows nothing about tokens/state. The browser's redirect to
http://localhost:18432/** is captured by GogoClaw's own callback handler; this
module only reports whether the browser reached that point.
"""

CALLBACK_URL_GLOB = "http://localhost:18432/**"
CONSENT_SELECTORS = [
    "button:has-text('Continue')",
    "button:has-text('Allow')",
    "#submit_approve_access",
]


def _default_launcher(*, headless, humanize, proxy):
    # Imported lazily so tests (which inject a fake launcher) don't need the real
    # stealth Chromium download.
    from cloakbrowser import launch

    return launch(headless=headless, humanize=humanize, proxy=proxy)


def drive(oauth_url, email, password, proxy=None, launcher=_default_launcher, headless=True):
    """Drive the browser to the OAuth callback. Returns {"ok": True} or
    {"ok": False, "reason": "..."}."""
    browser = launcher(headless=headless, humanize=True, proxy=proxy)
    try:
        page = browser.new_page()
        page.goto(oauth_url)
        page.locator("#identifierId").fill(email)
        page.locator("#identifierNext").click()
        page.locator('input[name="Passwd"]').fill(password)
        page.locator("#passwordNext").click()
        # A consent screen may or may not appear; best-effort, never fatal.
        for sel in CONSENT_SELECTORS:
            try:
                page.locator(sel).click()
            except Exception:
                pass
        page.wait_for_url(CALLBACK_URL_GLOB)
        return {"ok": True}
    except Exception as exc:  # wrong password, 2FA, captcha, timeout, …
        return {"ok": False, "reason": str(exc)}
    finally:
        try:
            browser.close()
        except Exception:
            pass
```
> Note: the consent-selector loop is best-effort — with the `FakeLocator` in tests, `.click()` succeeds harmlessly; at runtime a missing selector raises and is swallowed. `wait_for_url` is the real success gate.

Create `sidecar/server.py`:
```python
"""aiohttp server exposing the sidecar's /drive and /health endpoints."""
import asyncio

from aiohttp import web

from .driver import drive as default_drive

REQUIRED_FIELDS = ("oauth_url", "email", "password")


async def handle_drive(request):
    try:
        body = await request.json()
    except Exception:
        return web.json_response({"ok": False, "reason": "invalid json"}, status=400)
    missing = [f for f in REQUIRED_FIELDS if f not in body]
    if missing:
        return web.json_response({"ok": False, "reason": f"missing: {', '.join(missing)}"}, status=400)

    drive_fn = request.app["drive"]
    loop = asyncio.get_running_loop()
    result = await loop.run_in_executor(
        None,
        lambda: drive_fn(body["oauth_url"], body["email"], body["password"], body.get("proxy")),
    )
    return web.json_response(result)


async def handle_health(_request):
    return web.json_response({"ok": True})


def make_app(drive_fn=default_drive):
    app = web.Application()
    app["drive"] = drive_fn
    app.router.add_post("/drive", handle_drive)
    app.router.add_get("/health", handle_health)
    return app


def main():
    web.run_app(make_app(), host="127.0.0.1", port=31500)
```

Create `sidecar/__main__.py`:
```python
from sidecar.server import main

if __name__ == "__main__":
    main()
```

- [ ] **Step 5: Ignore Python caches**

Append to the repo-root `.gitignore`:
```
# Python
__pycache__/
.pytest_cache/
sidecar/.venv/
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /e/autoclaw && python -m pytest sidecar/test_sidecar.py -q`
Expected: PASS (6 tests: 2 driver + 3 endpoint + 1 health). No cloakbrowser import occurs (the default launcher is never hit because tests inject fakes / mock drive_fn).

- [ ] **Step 7: Commit**

```bash
git add sidecar/ .gitignore
git commit -m "feat(sidecar): CloakBrowser stealth drive over aiohttp /drive endpoint"
```

---

### Task 3: Sidecar process manager (`internal/sidecar/manager.go`)

**Files:**
- Create: `internal/sidecar/manager.go`
- Test: `internal/sidecar/manager_test.go`

**Interfaces:**
- Consumes: nothing (stdlib).
- Produces:
  - `sidecar.Manager` with `func New(python, scriptDir, addr string) *Manager`.
  - `func (m *Manager) URL() string` → `http://<addr>`.
  - `func (m *Manager) Ensure(ctx context.Context) error` — returns nil if the sidecar is already healthy; otherwise spawns `python -m sidecar` (cwd = scriptDir's parent so `sidecar` is importable) and waits up to ~10s for `/health`; idempotent under a mutex.
  - `func (m *Manager) Stop()` — kills the spawned process if any.

- [ ] **Step 1: Write the failing test**

Create `internal/sidecar/manager_test.go`:
```go
package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsure_NoSpawnWhenAlreadyHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	// python is a bogus command; if Ensure tried to spawn it would fail — but the
	// server is already healthy, so Ensure must short-circuit without spawning.
	addr := strings.TrimPrefix(srv.URL, "http://")
	m := New("this-command-does-not-exist", ".", addr)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure should short-circuit on a healthy sidecar, got %v", err)
	}
	if m.cmd != nil {
		t.Error("Ensure spawned a process despite a healthy sidecar")
	}
}

func TestEnsure_SpawnFailure(t *testing.T) {
	// Nothing is listening on this addr and the python command is bogus, so spawn
	// fails fast and Ensure returns an error (rather than hanging).
	m := New("this-command-does-not-exist", ".", "127.0.0.1:59999")
	if err := m.Ensure(context.Background()); err == nil {
		t.Error("expected Ensure to error when the sidecar can't be spawned")
	}
}

func TestURL(t *testing.T) {
	if got := New("python", ".", "127.0.0.1:31500").URL(); got != "http://127.0.0.1:31500" {
		t.Errorf("URL = %s", got)
	}
}
```
> Delete the empty `TestEnsure_SpawnFailureIsReported` stub if your linter flags it — it's a placeholder; the real case is `TestEnsure_SpawnFailure`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sidecar/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write the implementation**

Create `internal/sidecar/manager.go`:
```go
// Package sidecar manages the Python CloakBrowser sidecar subprocess.
package sidecar

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Manager spawns and health-checks the stealth-login sidecar on demand.
type Manager struct {
	python    string // python executable
	scriptDir string // working dir from which `python -m sidecar` is run
	addr      string // host:port the sidecar listens on
	http      *http.Client

	mu  sync.Mutex
	cmd *exec.Cmd
}

func New(python, scriptDir, addr string) *Manager {
	return &Manager{
		python: python, scriptDir: scriptDir, addr: addr,
		http: &http.Client{Timeout: 2 * time.Second},
	}
}

func (m *Manager) URL() string { return "http://" + m.addr }

func (m *Manager) healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL()+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Ensure guarantees a healthy sidecar, spawning `python -m sidecar` if needed.
func (m *Manager) Ensure(ctx context.Context) error {
	if m.healthy(ctx) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.healthy(ctx) { // re-check under lock
		return nil
	}
	cmd := exec.Command(m.python, "-m", "sidecar")
	cmd.Dir = m.scriptDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn sidecar: %w", err)
	}
	m.cmd = cmd

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m.healthy(ctx) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	m.cmd = nil
	return fmt.Errorf("sidecar did not become healthy at %s", m.addr)
}

// Stop terminates the spawned sidecar, if any.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		m.cmd = nil
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sidecar/ -v`
Expected: PASS (`TestEnsure_NoSpawnWhenAlreadyHealthy`, `TestEnsure_SpawnFailure`, `TestURL`).

- [ ] **Step 5: Commit**

```bash
git add internal/sidecar/
git commit -m "feat(sidecar): on-demand subprocess manager with health check"
```

---

### Task 4: Wire auto + bulk into the server (`internal/server`, `cmd/gogoclaw`)

**Files:**
- Modify: `internal/server/server.go`
- Modify: `cmd/gogoclaw/main.go`
- Test: `internal/server/server_test.go` (add cases)

**Interfaces:**
- Consumes: `auth.AuthEngine`, `auth.GoogleCred`, `auth.LoginDriver`, `sidecar.Manager` (via a small interface), `auth.AutoDriver`.
- Produces:
  - A server dependency `AutoLogin` interface: `Ensure(ctx context.Context) error` + `Driver() auth.LoginDriver`. `server.New` gains an `autoLogin AutoLogin` param (nil-able — nil ⇒ `mode:"auto"` still returns 501).
  - `handleLoginStart` `mode:"auto"`: when `autoLogin != nil`, `Ensure` the sidecar then `StartLogin(autoLogin.Driver(), cred)` using `req.Cred`.
  - New route `POST /api/login/bulk` `{accounts:[{email,password,proxy?}]}` → ensures the sidecar once, starts each login with bounded concurrency (max 3), returns `{started:[{email,state}], errors:[{email,error}]}`.
  - `main.go`: build a `sidecar.Manager` + `auth.NewAutoDriver(manager.URL())`, wrap them in a concrete `autoLogin`, pass to `server.New`; `Stop()` the manager on shutdown.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go`:
```go
// fakeAutoLogin drives logins through a stub driver without a real sidecar.
type fakeAutoLogin struct {
	ensured bool
	driver  auth.LoginDriver
}

func (f *fakeAutoLogin) Ensure(context.Context) error { f.ensured = true; return nil }
func (f *fakeAutoLogin) Driver() auth.LoginDriver      { return f.driver }

// stubDriver is a LoginDriver that succeeds immediately (no browser).
type stubDriver struct{}

func (stubDriver) Drive(context.Context, string, *auth.GoogleCred) error { return nil }

func newServerWithAuto(t *testing.T, autoglm http.HandlerFunc, al AutoLogin) http.Handler {
	srv := httptest.NewServer(autoglm)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := api.NewClientWithBase(srv.URL)
	bus := events.New()
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus, al).Handler()
}

func TestLoginStart_AutoUsesSidecarWhenConfigured(t *testing.T) {
	al := &fakeAutoLogin{driver: stubDriver{}}
	h := newServerWithAuto(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-a"}})
	}, al)
	rec := httptest.NewRecorder()
	body := `{"mode":"auto","cred":{"Email":"a@x.com","Password":"pw"}}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/start", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	if !al.ensured {
		t.Error("Ensure was not called for auto login")
	}
}

func TestBulkLogin_StartsEachAccount(t *testing.T) {
	al := &fakeAutoLogin{driver: stubDriver{}}
	var n int
	h := newServerWithAuto(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"oauth_url": "u", "state": "st-" + strings.Repeat("x", n)}})
	}, al)
	rec := httptest.NewRecorder()
	body := `{"accounts":[{"email":"a@x.com","password":"p1"},{"email":"b@x.com","password":"p2"}]}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/bulk", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	var out struct {
		Started []struct {
			Email string `json:"email"`
			State string `json:"state"`
		} `json:"started"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Started) != 2 {
		t.Errorf("started = %d, want 2 (%s)", len(out.Started), rec.Body)
	}
}

func TestLoginStart_AutoStill501WhenUnconfigured(t *testing.T) {
	h, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/login/start", strings.NewReader(`{"mode":"auto"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("code = %d, want 501 when auto-login unconfigured", rec.Code)
	}
}
```
Also update the existing `newServer` helper to pass `nil` for the new `autoLogin` param:
```go
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus, nil).Handler(), st
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run 'Auto|Bulk' -v`
Expected: FAIL — `New` arg count mismatch / `undefined: AutoLogin`.

- [ ] **Step 3: Update the server**

In `internal/server/server.go`, add the interface and field, extend `New`, and wire the handlers. Add near the top:
```go
// AutoLogin drives stealth auto-logins via the sidecar (Plan 4). Nil disables auto mode.
type AutoLogin interface {
	Ensure(ctx context.Context) error
	Driver() auth.LoginDriver
}
```
Add `"context"` to the imports (now used by the interface). Change `Server` + `New`:
```go
type Server struct {
	engine    *auth.AuthEngine
	refresher *refresh.Refresher
	store     store.Store
	bus       *events.Bus
	autoLogin AutoLogin
}

func New(engine *auth.AuthEngine, refresher *refresh.Refresher, st store.Store, bus *events.Bus, autoLogin AutoLogin) *Server {
	return &Server{engine: engine, refresher: refresher, store: st, bus: bus, autoLogin: autoLogin}
}
```
Register the bulk route in `Handler()` (next to the other `/api/login` routes):
```go
	mux.HandleFunc("POST /api/login/bulk", s.handleBulkLogin)
```
Replace the `mode == "auto"` branch in `handleLoginStart`:
```go
	if req.Mode == "auto" {
		if s.autoLogin == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auto login not configured"})
			return
		}
		if err := s.autoLogin.Ensure(r.Context()); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		state, url, err := s.engine.StartLogin(r.Context(), s.autoLogin.Driver(), req.Cred)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": state, "oauth_url": url})
		return
	}
```
Add the bulk handler at the end of the file:
```go
func (s *Server) handleBulkLogin(w http.ResponseWriter, r *http.Request) {
	if s.autoLogin == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auto login not configured"})
		return
	}
	var req struct {
		Accounts []auth.GoogleCred `json:"accounts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Accounts) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected non-empty accounts array"})
		return
	}
	if err := s.autoLogin.Ensure(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	type startedItem struct {
		Email string `json:"email"`
		State string `json:"state"`
	}
	type errItem struct {
		Email string `json:"email"`
		Error string `json:"error"`
	}
	var (
		mu      sync.Mutex
		started []startedItem
		errs    []errItem
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 3) // bounded concurrency
	)
	driver := s.autoLogin.Driver()
	for i := range req.Accounts {
		cred := req.Accounts[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			state, _, err := s.engine.StartLogin(r.Context(), driver, &cred)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, errItem{Email: cred.Email, Error: err.Error()})
				return
			}
			started = append(started, startedItem{Email: cred.Email, State: state})
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"started": started, "errors": errs})
}
```
Add `"sync"` to the imports.

- [ ] **Step 4: Wire `main.go`**

In `cmd/gogoclaw/main.go`, add a concrete `AutoLogin` and pass it to `server.New`. Add imports `gogoclaw/internal/sidecar`, `gogoclaw/internal/auth`'s `AutoDriver` (auth is already imported), and `os/exec` (to resolve the python path). Add this adapter (its `Driver()` returns `auth.LoginDriver` — `*auth.AutoDriver` implements that interface, so it satisfies `server.AutoLogin`):
```go
// autoLogin adapts the sidecar manager + AutoDriver to server.AutoLogin.
type autoLogin struct {
	mgr    *sidecar.Manager
	driver *auth.AutoDriver
}

func (a *autoLogin) Ensure(ctx context.Context) error { return a.mgr.Ensure(ctx) }
func (a *autoLogin) Driver() auth.LoginDriver          { return a.driver }
```

Update `buildHandler` and `main`:
```go
func buildHandler(st store.Store, c *api.Client, bus *events.Bus, al server.AutoLogin) (http.Handler, *refresh.Refresher) {
	engine := auth.New(c, st, bus)
	refresher := refresh.New(c, st, bus)
	srv := server.New(engine, refresher, st, bus, al)
	return srv.Handler(), refresher
}
```
In `main()`, before `buildHandler`, build the auto-login (find python, sidecar dir = repo root so `python -m sidecar` imports the `sidecar` package):
```go
	pyPath := "python"
	if p, err := exec.LookPath("python"); err == nil {
		pyPath = p
	}
	mgr := sidecar.New(pyPath, ".", "127.0.0.1:31500")
	al := &autoLogin{mgr: mgr, driver: auth.NewAutoDriver(mgr.URL())}
	defer mgr.Stop()

	handler, refresher := buildHandler(st, api.NewClient(), bus, al)
```
And update `main_test.go`'s `buildHandler` call to pass `nil` for the new param:
```go
	h, refresher := buildHandler(st, api.NewClient(), events.New(), nil)
```

- [ ] **Step 5: Run tests to verify they pass**

Run:
```bash
go test ./internal/server/ ./cmd/gogoclaw/ -v && go build ./... && go vet ./...
```
Expected: PASS; build + vet clean. (The auto/bulk server tests pass via the stub driver; the manual-login and 501-when-unconfigured tests still pass.)

- [ ] **Step 6: Commit**

```bash
git add internal/server/ cmd/gogoclaw/
git commit -m "feat(server): wire stealth auto-login + bulk endpoint via sidecar"
```

---

### Task 5: Bulk-login UI (`web/src/components/BulkLogin.tsx`)

**Files:**
- Create: `web/src/components/BulkLogin.tsx`
- Modify: `web/src/lib/api.ts` (add `bulkLogin`), `web/src/App.tsx` (enable the Bulk button → open the modal)
- Test: `web/src/components/BulkLogin.test.tsx`
- Modify: `web/src/App.test.tsx` (the Bulk button is no longer disabled)

**Interfaces:**
- Consumes: existing `api.ts` `req` pattern, SSE-driven `["accounts"]` invalidation.
- Produces:
  - `api.ts`: `bulkLogin(creds: {email:string;password:string}[]) : Promise<{started:{email:string;state:string}[]; errors:{email:string;error:string}[]}>` POSTing `/api/login/bulk`.
  - `BulkLogin({onClose})` — a modal with a textarea for `email:password` lines; on submit parses lines, calls `bulkLogin`, shows a per-account started/error summary. New accounts arrive in the table via the existing SSE invalidation.
  - `App.tsx`: the "Bulk login" button is enabled and opens `BulkLogin`.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/BulkLogin.test.tsx`:
```tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { BulkLogin } from "./BulkLogin";

afterEach(() => vi.restoreAllMocks());

test("parses email:password lines and posts them, showing results", async () => {
  const fetchMock = vi.fn(async () => new Response(
    JSON.stringify({ started: [{ email: "a@x.com", state: "s1" }, { email: "b@x.com", state: "s2" }], errors: [] }),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} />);
  await userEvent.type(
    screen.getByLabelText(/credentials/i),
    "a@x.com:pw1\nb@x.com:pw2",
  );
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.objectContaining({ method: "POST" })));
  const sent = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(sent.accounts).toEqual([
    { email: "a@x.com", password: "pw1" },
    { email: "b@x.com", password: "pw2" },
  ]);
  await waitFor(() => expect(screen.getByText(/2 started/i)).toBeInTheDocument());
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /e/autoclaw/web && npx vitest run src/components/BulkLogin.test.tsx`
Expected: FAIL — cannot resolve `./BulkLogin`.

- [ ] **Step 3: Add the API call**

Append to `web/src/lib/api.ts`:
```ts
export interface BulkResult {
  started: { email: string; state: string }[];
  errors: { email: string; error: string }[];
}

export async function bulkLogin(
  creds: { email: string; password: string }[],
): Promise<BulkResult> {
  return (await req("/api/login/bulk", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ accounts: creds }),
  })) as BulkResult;
}
```

- [ ] **Step 4: Write the modal**

Create `web/src/components/BulkLogin.tsx`:
```tsx
import { XIcon } from "@phosphor-icons/react";
import { useState } from "react";
import { bulkLogin, type BulkResult } from "../lib/api";

// parseCreds turns "email:password" lines into credential objects, skipping blanks.
export function parseCreds(text: string): { email: string; password: string }[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .map((line) => {
      const idx = line.indexOf(":");
      return { email: line.slice(0, idx).trim(), password: line.slice(idx + 1).trim() };
    })
    .filter((c) => c.email && c.password);
}

export function BulkLogin({ onClose }: { onClose: () => void }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<BulkResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    setError(null);
    const creds = parseCreds(text);
    if (creds.length === 0) {
      setError("Enter at least one email:password line");
      return;
    }
    setBusy(true);
    try {
      setResult(await bulkLogin(creds));
    } catch (e) {
      setError(e instanceof Error ? e.message : "bulk login failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="w-full max-w-lg rounded-xl border border-border bg-surface p-6">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold">Bulk stealth login</h2>
          <button type="button" aria-label="Close" onClick={onClose} className="rounded-md p-1 text-muted hover:text-ink">
            <XIcon size={20} />
          </button>
        </div>
        <label htmlFor="bulk-creds" className="mb-2 block text-sm text-muted">
          Google credentials — one <span className="text-ink">email:password</span> per line
        </label>
        <textarea
          id="bulk-creds" value={text} onChange={(e) => setText(e.target.value)} rows={8}
          className="w-full rounded-lg border border-border bg-panel p-3 font-mono text-sm text-ink outline-none focus-visible:border-brand"
          placeholder={"you@gmail.com:password\n…"}
        />
        <p className="mt-2 text-xs text-muted">Credentials are used once for sign-in and never stored.</p>

        {error && <p className="mt-3 text-sm text-err">{error}</p>}
        {result && (
          <p className="mt-3 text-sm text-muted">
            <span className="text-ok">{result.started.length} started</span>
            {result.errors.length > 0 && <span className="text-err"> · {result.errors.length} failed</span>}
          </p>
        )}

        <div className="mt-5 flex justify-end gap-2">
          <button type="button" onClick={onClose} className="rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink">
            Close
          </button>
          <button
            type="button" onClick={submit} disabled={busy}
            className="rounded-lg bg-brand px-3 py-2 text-sm font-medium text-brand-ink hover:brightness-110 disabled:opacity-50"
          >
            {busy ? "Starting…" : "Start bulk login"}
          </button>
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 5: Run the modal test to verify it passes**

Run: `cd /e/autoclaw/web && npx vitest run src/components/BulkLogin.test.tsx`
Expected: PASS.

- [ ] **Step 6: Enable the Bulk button in App**

In `web/src/App.tsx`, add `import { BulkLogin } from "./components/BulkLogin";` and a `useState` for the modal, replace the disabled Bulk button, and render the modal. Inside `Dashboard`:
```tsx
  const [bulkOpen, setBulkOpen] = useState(false);
```
(add `import { useState } from "react";` if not already imported). Replace the disabled Bulk button with:
```tsx
          <button
            type="button" onClick={() => setBulkOpen(true)}
            className="rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-brand"
          >
            Bulk login
          </button>
```
And before the closing `</div>` of the dashboard root, render:
```tsx
      {bulkOpen && <BulkLogin onClose={() => setBulkOpen(false)} />}
```

- [ ] **Step 7: Update the App test (Bulk button now enabled)**

In `web/src/App.test.tsx`, the second test asserts the Bulk login button is disabled — change it to assert it is now enabled (present and clickable):
```tsx
  await waitFor(() => expect(screen.getByText("a@x.com")).toBeInTheDocument());
  expect(screen.getByRole("button", { name: /bulk login/i })).toBeEnabled();
```

- [ ] **Step 8: Test, build, verify embed**

Run:
```bash
cd /e/autoclaw/web && npx vitest run && npx tsc --noEmit && npm run build
cd /e/autoclaw && go build ./... && go test ./... -count=1
```
Expected: all Vitest tests pass; `tsc` clean; build refreshes `web/dist` (confirm no `web/vite.config.js` shadow in the web root); Go builds; full Go suite green.

- [ ] **Step 9: Commit (including the rebuilt dist)**

```bash
cd /e/autoclaw
git add web/src web/dist
git commit -m "feat(web): bulk stealth-login modal wired to /api/login/bulk"
```

---

## Notes for the implementer

- **CloakBrowser is never imported in tests.** `driver.py`'s default launcher imports `cloakbrowser` lazily; every test injects a fake launcher or a stub `drive_fn`, so `python -m pytest` needs only `requirements-dev.txt` (aiohttp, pytest, pytest-aiohttp). The real stealth Chromium is only needed for the manual end-to-end smoke.
- **Manual smoke (documented, not automated):** with `cloakbrowser` installed (`python -m pip install -r sidecar/requirements.txt`), run `go run ./cmd/gogoclaw`, open the dashboard, click Bulk login, paste a real `email:password`, and Start — GogoClaw auto-spawns the sidecar, CloakBrowser drives Google, the redirect hits `/auth/callback-google`, and the account appears via SSE. The sidecar can also be run standalone for debugging: `python -m sidecar`.
- **The sidecar stays stateless** — it reports `{ok, reason}` only; all token handling remains in `AuthEngine.HandleCallback`. This is the convergence point shared with the manual driver.
- **Credentials never persist** — no store column; they live only in the request path and are dropped after each drive.
- **Bounded concurrency (3)** in the bulk handler avoids Google bot-flagging; the sidecar itself is stateless so concurrent `/drive` calls each get their own browser.
- Deferred (from the design's open items and Plan 3 final review): per-account proxy UI, per-event SSE branching (toasts on `login:error`), sortable-header keyboard a11y, and a CI check that rebuilds `web/dist`.
