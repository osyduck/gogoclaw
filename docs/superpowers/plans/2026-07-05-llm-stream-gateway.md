# LLM Stream Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the pool of AutoClaw accounts as a local OpenAI- and Anthropic-compatible streaming LLM endpoint, forwarding each request upstream with a rotated account JWT (modes: sticky / rotate-after-N / round-robin) and auto-failover.

**Architecture:** A new `internal/proxy` package mounts `/v1/*` routes on the existing HTTP server (`127.0.0.1:18432`). A `Selector` picks an account per request from the store per the configured mode; a `Forwarder` sends the upstream POST to `/autoclaw-proxy/proxy/autoclaw/chat/completions` injecting `X-Authorization` and retrying on other accounts (≤5) on retryable errors. OpenAI requests stream through verbatim; Anthropic `/v1/messages` requests are translated both ways. Rotation settings persist in a new sqlite table, editable from the dashboard.

**Tech Stack:** Go 1.25 (`net/http`, `modernc.org/sqlite`), React + TypeScript + Vite + TanStack Query (existing dashboard).

## Global Constraints

- Module path: `gogoclaw`. Go version floor: `go 1.25.0`.
- Upstream base host: `https://autoglm-api.autoglm.ai` (available as `api.BaseURL`). Upstream chat path: `/autoclaw-proxy/proxy/autoclaw/chat/completions`.
- Fixed upstream token: `authorization: Bearer autoclaw-internal-proxy`. Per-account token: `X-Authorization: Bearer <access_token>`.
- Model header/body split: header `X-Request-Model: <prefix>_<bare>`, body `"model":"<bare>"`.
- Static model catalog seed (friendly → prefix / bare): `glm-5.2`→`openrouter`/`glm-5.2`; `glm-5-turbo`→`zai`/`glm-5-turbo`; `auto`→`zai`/`auto`.
- Eligible account = `status == "active"` AND `balance > 0` (`store.StatusActive`).
- Failover retryable statuses: `401, 402, 403, 429`, any `5xx`, and transport errors. Max **5** attempts total. Non-retryable statuses (e.g. `400`) surface as-is. Failover only before the first response byte is streamed.
- Existing store `List()` returns accounts `ORDER BY added_at`; may return `nil` for empty.
- Do not forward the client's `Authorization`/`x-api-key` to upstream. Bind stays localhost.
- Prefer pure-Go, no new Go dependencies. Follow existing file style (focused files, table-driven tests, `httptest` for HTTP).

---

## File Structure

- `internal/store/store.go` — **modify**: add `ProxyConfig` type + interface methods.
- `internal/store/sqlite.go` — **modify**: `proxy_settings` table + `GetProxyConfig`/`SetProxyConfig`.
- `internal/api/sign.go` — **modify**: export header-value constants (`Version`, `Product`, `TM`, `Channel`, `Lang`, `UserAgent`) for reuse by the proxy.
- `internal/proxy/selector.go` — rotation engine (`Selector`, `Pick`, `Next`, `Current`).
- `internal/proxy/catalog.go` — static model catalog (`Lookup`, `Models`, `Route`).
- `internal/proxy/upstream.go` — `Forwarder` (`Do`, `Forward` with failover), `Picker` interface.
- `internal/proxy/gateway.go` — `Gateway` struct, route registration, proxy-auth middleware, `/api/proxy/config`.
- `internal/proxy/openai.go` — `/v1/chat/completions`, `/v1/models` handlers.
- `internal/proxy/anthropic_request.go` — Anthropic→OpenAI request translation.
- `internal/proxy/anthropic_stream.go` — OpenAI-SSE→Anthropic-SSE stream translation + `/v1/messages` handler.
- `internal/proxy/*_test.go` — tests per unit; HAR-derived fixtures under `internal/proxy/testdata/`.
- `internal/server/server.go` — **modify**: accept `*proxy.Gateway`, mount its routes.
- `cmd/gogoclaw/main.go` — **modify**: construct the gateway, pass to `server.New`.
- `web/src/lib/types.ts`, `web/src/lib/api.ts`, `web/src/hooks/useProxyConfig.ts`, `web/src/components/GatewayPanel.tsx`, `web/src/App.tsx` — dashboard config panel.

---

## Task 1: Proxy settings persistence

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/sqlite.go`
- Test: `internal/store/sqlite_test.go`

**Interfaces:**
- Produces: `store.ProxyConfig{Mode string; N int; APIKey string}`; `store.Store.GetProxyConfig() (ProxyConfig, error)`; `store.Store.SetProxyConfig(ProxyConfig) error`. Mode values: `"sticky"`, `"round_robin"`, `"rotate_after_n"`. Defaults when unset: `{Mode:"round_robin", N:5, APIKey:""}`.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/sqlite_test.go`:

```go
func TestProxyConfigDefaultsAndRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Defaults when never set.
	got, err := st.GetProxyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "round_robin" || got.N != 5 || got.APIKey != "" {
		t.Fatalf("defaults = %+v", got)
	}

	// Round-trip.
	want := ProxyConfig{Mode: "rotate_after_n", N: 3, APIKey: "secret"}
	if err := st.SetProxyConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetProxyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestProxyConfig -v`
Expected: FAIL — `st.GetProxyConfig undefined`.

- [ ] **Step 3: Add the type and interface methods**

In `internal/store/store.go`, add after the `Account` struct:

```go
// ProxyConfig holds the LLM-gateway rotation settings (single row).
type ProxyConfig struct {
	Mode   string // "sticky" | "round_robin" | "rotate_after_n"
	N      int    // request count per account for rotate_after_n
	APIKey string // client key required on /v1/*; empty = open
}
```

In the `Store` interface add:

```go
	GetProxyConfig() (ProxyConfig, error)
	SetProxyConfig(ProxyConfig) error
```

- [ ] **Step 4: Implement in sqlite.go**

In `internal/store/sqlite.go`, append to the `migrations` slice a new statement:

```go
	`CREATE TABLE IF NOT EXISTS proxy_settings (
	  id      INTEGER PRIMARY KEY CHECK (id = 1),
	  mode    TEXT NOT NULL,
	  n       INTEGER NOT NULL,
	  api_key TEXT NOT NULL
	)`,
```

(The `duplicate column` guard in `Open` also tolerates re-running `CREATE TABLE IF NOT EXISTS`, which is a no-op.)

Add methods at the end of the file:

```go
func (s *SQLiteStore) GetProxyConfig() (ProxyConfig, error) {
	row := s.db.QueryRow(`SELECT mode, n, api_key FROM proxy_settings WHERE id = 1`)
	var c ProxyConfig
	err := row.Scan(&c.Mode, &c.N, &c.APIKey)
	if err == sql.ErrNoRows {
		return ProxyConfig{Mode: "round_robin", N: 5, APIKey: ""}, nil
	}
	if err != nil {
		return ProxyConfig{}, err
	}
	return c, nil
}

func (s *SQLiteStore) SetProxyConfig(c ProxyConfig) error {
	_, err := s.db.Exec(`
INSERT INTO proxy_settings (id, mode, n, api_key) VALUES (1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET mode=excluded.mode, n=excluded.n, api_key=excluded.api_key`,
		c.Mode, c.N, c.APIKey)
	return err
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS (including existing tests).

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): persist LLM-gateway proxy config (mode/n/api_key)"
```

---

## Task 2: Rotation selector

**Files:**
- Create: `internal/proxy/selector.go`
- Test: `internal/proxy/selector_test.go`

**Interfaces:**
- Consumes: `store.Store` (`List()`, `GetProxyConfig()`), `store.Account`, `store.StatusActive`.
- Produces:
  - `proxy.ErrNoEligible` (sentinel `error`).
  - `type Selector struct{...}`; `proxy.NewSelector(st store.Store) *Selector`.
  - `(*Selector) Pick() (store.Account, error)` — account for a new request per current mode.
  - `(*Selector) Next(tried map[string]bool) (store.Account, error)` — next eligible account not in `tried` (failover); errors when exhausted.
  - `(*Selector) Current() string` — email last returned (for UI); `""` if none.

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/selector_test.go`:

```go
package proxy

import (
	"testing"

	"gogoclaw/internal/store"
)

// memStore is an in-memory Store for selector tests.
type memStore struct {
	accts []store.Account
	cfg   store.ProxyConfig
}

func (m *memStore) List() ([]store.Account, error)          { return m.accts, nil }
func (m *memStore) GetProxyConfig() (store.ProxyConfig, error) { return m.cfg, nil }

// Unused Store methods (selector only needs List + GetProxyConfig).
func (m *memStore) Add(store.Account) error                          { return nil }
func (m *memStore) Get(string) (store.Account, error)               { return store.Account{}, nil }
func (m *memStore) UpdateTokens(string, string, string, t1, t2 any) error { return nil }
func (m *memStore) SetStatus(string, string) error                  { return nil }
func (m *memStore) UpdateBalance(string, int) error                 { return nil }
func (m *memStore) Delete(string) error                             { return nil }
func (m *memStore) SetProxyConfig(store.ProxyConfig) error          { return nil }

func acct(email string, bal int, status string) store.Account {
	return store.Account{Email: email, Balance: bal, Status: status}
}

func eligiblePool() []store.Account {
	return []store.Account{
		acct("a@x.com", 10, store.StatusActive),
		acct("b@x.com", 0, store.StatusActive),               // ineligible: zero balance
		acct("c@x.com", 10, store.StatusNeedsRelogin),         // ineligible: status
		acct("d@x.com", 10, store.StatusActive),
	}
}

func TestPickSkipsIneligible(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "sticky", N: 5}}
	s := NewSelector(m)
	got, err := s.Pick()
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "a@x.com" {
		t.Fatalf("sticky first pick = %s, want a@x.com", got.Email)
	}
	// Sticky stays put.
	got, _ = s.Pick()
	if got.Email != "a@x.com" {
		t.Fatalf("sticky second pick = %s, want a@x.com", got.Email)
	}
}

func TestPickRoundRobin(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "round_robin"}}
	s := NewSelector(m)
	var seq []string
	for i := 0; i < 4; i++ {
		g, _ := s.Pick()
		seq = append(seq, g.Email)
	}
	// Only a@ and d@ are eligible; round-robin alternates.
	want := []string{"a@x.com", "d@x.com", "a@x.com", "d@x.com"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("round-robin seq = %v, want %v", seq, want)
		}
	}
}

func TestPickRotateAfterN(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "rotate_after_n", N: 2}}
	s := NewSelector(m)
	var seq []string
	for i := 0; i < 5; i++ {
		g, _ := s.Pick()
		seq = append(seq, g.Email)
	}
	want := []string{"a@x.com", "a@x.com", "d@x.com", "d@x.com", "a@x.com"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("rotate-after-2 seq = %v, want %v", seq, want)
		}
	}
}

func TestNextFailover(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "sticky"}}
	s := NewSelector(m)
	first, _ := s.Pick() // a@x.com
	tried := map[string]bool{first.Email: true}
	nxt, err := s.Next(tried)
	if err != nil {
		t.Fatal(err)
	}
	if nxt.Email != "d@x.com" {
		t.Fatalf("failover = %s, want d@x.com", nxt.Email)
	}
	tried[nxt.Email] = true
	if _, err := s.Next(tried); err == nil {
		t.Fatal("expected exhaustion error when all eligible tried")
	}
}

func TestPickEmptyPool(t *testing.T) {
	m := &memStore{accts: nil, cfg: store.ProxyConfig{Mode: "sticky"}}
	s := NewSelector(m)
	if _, err := s.Pick(); err != ErrNoEligible {
		t.Fatalf("empty pool err = %v, want ErrNoEligible", err)
	}
}
```

> Note: the `UpdateTokens` stub signature above is a simplified placeholder for the test's unused method; match the real interface signature `UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error` by importing `"time"` and using it — see Step 3 note.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run TestPick -v`
Expected: FAIL — `NewSelector undefined` (and compile error).

- [ ] **Step 3: Implement the selector**

Create `internal/proxy/selector.go`:

```go
// Package proxy is the local OpenAI/Anthropic-compatible LLM gateway that
// forwards requests to the AutoClaw upstream using a rotated account token.
package proxy

import (
	"errors"
	"sync"

	"gogoclaw/internal/store"
)

// ErrNoEligible means no account is currently usable (active + positive balance).
var ErrNoEligible = errors.New("no eligible accounts")

// picker is the subset of store the selector needs.
type selectorStore interface {
	List() ([]store.Account, error)
	GetProxyConfig() (store.ProxyConfig, error)
}

// Selector chooses an account per request according to the configured mode.
type Selector struct {
	st selectorStore

	mu     sync.Mutex
	cursor string // email of the currently pinned account
	count  int    // requests served on the current account (rotate_after_n)
}

func NewSelector(st selectorStore) *Selector { return &Selector{st: st} }

// eligible returns accounts that are active with positive balance, preserving
// store order (added_at).
func (s *Selector) eligible() ([]store.Account, error) {
	all, err := s.st.List()
	if err != nil {
		return nil, err
	}
	out := make([]store.Account, 0, len(all))
	for _, a := range all {
		if a.Status == store.StatusActive && a.Balance > 0 {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoEligible
	}
	return out, nil
}

// indexOf returns the position of email in list, or -1.
func indexOf(list []store.Account, email string) int {
	for i, a := range list {
		if a.Email == email {
			return i
		}
	}
	return -1
}

// Pick returns the account to use for a new client request.
func (s *Selector) Pick() (store.Account, error) {
	cfg, err := s.st.GetProxyConfig()
	if err != nil {
		return store.Account{}, err
	}
	list, err := s.eligible()
	if err != nil {
		return store.Account{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := indexOf(list, s.cursor)
	switch cfg.Mode {
	case "round_robin":
		if idx < 0 {
			idx = 0
		} else {
			idx = (idx + 1) % len(list)
		}
	case "rotate_after_n":
		n := cfg.N
		if n < 1 {
			n = 1
		}
		if idx < 0 {
			idx, s.count = 0, 1
		} else {
			s.count++
			if s.count > n {
				s.count = 1
				idx = (idx + 1) % len(list)
			}
		}
	default: // "sticky"
		if idx < 0 {
			idx = 0
		}
	}
	chosen := list[idx]
	s.cursor = chosen.Email
	return chosen, nil
}

// Next advances to the next eligible account not present in tried (failover).
func (s *Selector) Next(tried map[string]bool) (store.Account, error) {
	list, err := s.eligible()
	if err != nil {
		return store.Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	start := indexOf(list, s.cursor)
	if start < 0 {
		start = 0
	}
	for i := 1; i <= len(list); i++ {
		cand := list[(start+i)%len(list)]
		if !tried[cand.Email] {
			s.cursor = cand.Email
			s.count = 1
			return cand, nil
		}
	}
	return store.Account{}, errors.New("all eligible accounts exhausted")
}

// Current returns the email of the account most recently chosen ("" if none).
func (s *Selector) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}
```

> Test note: in `selector_test.go`, make `memStore` satisfy the full `store.Store` interface so it can also be reused later. Import `"time"` and give `UpdateTokens` the exact signature `func (m *memStore) UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error { return nil }`. `NewSelector` accepts the narrower `selectorStore`, which `*memStore` satisfies.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS (all selector tests).

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/selector.go internal/proxy/selector_test.go
git commit -m "feat(proxy): account rotation selector (sticky/round-robin/rotate-after-n + failover)"
```

---

## Task 3: Model catalog

**Files:**
- Create: `internal/proxy/catalog.go`
- Test: `internal/proxy/catalog_test.go`

**Interfaces:**
- Produces: `type Route struct{ Prefix, Bare string }`; `(Route) Prefixed() string` (returns `Prefix+"_"+Bare`); `proxy.Lookup(model string) (Route, bool)`; `proxy.Models() []string` (sorted friendly ids).

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/catalog_test.go`:

```go
package proxy

import "testing"

func TestCatalogLookup(t *testing.T) {
	r, ok := Lookup("glm-5.2")
	if !ok || r.Prefix != "openrouter" || r.Bare != "glm-5.2" {
		t.Fatalf("glm-5.2 = %+v ok=%v", r, ok)
	}
	if r.Prefixed() != "openrouter_glm-5.2" {
		t.Fatalf("prefixed = %s", r.Prefixed())
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown model should not resolve")
	}
}

func TestCatalogModels(t *testing.T) {
	got := Models()
	if len(got) < 3 {
		t.Fatalf("expected >=3 models, got %v", got)
	}
	// Sorted, contains seed ids.
	found := map[string]bool{}
	for _, m := range got {
		found[m] = true
	}
	for _, want := range []string{"glm-5.2", "glm-5-turbo", "auto"} {
		if !found[want] {
			t.Fatalf("catalog missing %s: %v", want, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run TestCatalog -v`
Expected: FAIL — `Lookup undefined`.

- [ ] **Step 3: Implement the catalog**

Create `internal/proxy/catalog.go`:

```go
package proxy

import "sort"

// Route maps a friendly model name to the upstream provider prefix and the bare
// model id sent in the request body.
type Route struct {
	Prefix string
	Bare   string
}

// Prefixed returns the value for the X-Request-Model header, e.g. "openrouter_glm-5.2".
func (r Route) Prefixed() string { return r.Prefix + "_" + r.Bare }

// catalog is the static, editable model table. Add a line here to expose a new
// AutoClaw model. Keys are the friendly ids clients send as "model".
var catalog = map[string]Route{
	"glm-5.2":     {Prefix: "openrouter", Bare: "glm-5.2"},
	"glm-5-turbo": {Prefix: "zai", Bare: "glm-5-turbo"},
	"auto":        {Prefix: "zai", Bare: "auto"},
}

// Lookup resolves a friendly model name to its Route.
func Lookup(model string) (Route, bool) {
	r, ok := catalog[model]
	return r, ok
}

// Models returns the sorted list of friendly model ids for GET /v1/models.
func Models() []string {
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -run TestCatalog -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/catalog.go internal/proxy/catalog_test.go
git commit -m "feat(proxy): static model catalog with prefix/bare split"
```

---

## Task 4: Upstream forwarder with failover

**Files:**
- Modify: `internal/api/sign.go` (export header-value constants)
- Create: `internal/proxy/upstream.go`
- Test: `internal/proxy/upstream_test.go`

**Interfaces:**
- Consumes: `store.Account` (`AccessToken`), `api.BaseURL`, exported `api.Version`, `api.Product`, `api.TM`, `api.Channel`, `api.Lang`, `api.UserAgent`.
- Produces:
  - `type Picker interface { Pick() (store.Account, error); Next(tried map[string]bool) (store.Account, error) }` (implemented by `*Selector`).
  - `type Forwarder struct{...}`; `proxy.NewForwarder(base string) *Forwarder`.
  - `(*Forwarder) Do(ctx context.Context, acct store.Account, prefixedModel string, body []byte) (*http.Response, error)` — one attempt.
  - `(*Forwarder) Forward(ctx context.Context, p Picker, prefixedModel string, body []byte) (*http.Response, store.Account, error)` — failover loop; returns a live (unread) response whose body the caller must close.

- [ ] **Step 1: Export the shared header constants in api**

In `internal/api/sign.go`, change the const block so the header VALUES are exported (values unchanged), and update `signHeaders` to reference them:

```go
const (
	BaseURL  = "https://autoglm-api.autoglm.ai"
	SourceID = "autoclaw"

	appID  = "100003"
	appKey = "38d2391985e2369a5fb8227d8e6cd5e5"

	// Exported so the proxy package can build the same client-identity headers.
	Version = "1.10.3"
	Product = "autoclaw"
	TM      = "win"
	Channel = "official"
	Lang    = "en"

	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) autoclaw/1.10.3 Chrome/130.0.6723.191 Electron/33.4.11 Safari/537.36"
)
```

Update `signHeaders` body to use the exported names:

```go
	h.Set("x-version", Version)
	h.Set("x-tm", TM)
	h.Set("x-product", Product)
	h.Set("x-channel", Channel)
	h.Set("x-lang", Lang)
	h.Set("user-agent", UserAgent)
```

Run `go test ./internal/api/ -v` — expected PASS (values unchanged; `TestSignHeaders` still sees `x-product=autoclaw`, `x-version=1.10.3`).

- [ ] **Step 2: Write the failing test**

Create `internal/proxy/upstream_test.go`:

```go
package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogoclaw/internal/store"
)

// fakePicker drives Forward with a scripted account sequence.
type fakePicker struct {
	accts []store.Account
	i     int
}

func (f *fakePicker) Pick() (store.Account, error) {
	if f.i >= len(f.accts) {
		return store.Account{}, ErrNoEligible
	}
	a := f.accts[f.i]
	return a, nil
}
func (f *fakePicker) Next(tried map[string]bool) (store.Account, error) {
	f.i++
	if f.i >= len(f.accts) {
		return store.Account{}, ErrNoEligible
	}
	return f.accts[f.i], nil
}

func TestDoSetsUpstreamHeaders(t *testing.T) {
	var gotAuth, gotXAuth, gotModel, gotInternal string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInternal = r.Header.Get("authorization")
		gotXAuth = r.Header.Get("X-Authorization")
		gotModel = r.Header.Get("X-Request-Model")
		gotAuth = r.Header.Get("X-Product")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	f := NewForwarder(srv.URL)
	resp, err := f.Do(context.Background(),
		store.Account{Email: "a@x.com", AccessToken: "JWT123"},
		"openrouter_glm-5.2", []byte(`{"model":"glm-5.2"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if gotInternal != "Bearer autoclaw-internal-proxy" {
		t.Errorf("internal auth = %q", gotInternal)
	}
	if gotXAuth != "Bearer JWT123" {
		t.Errorf("x-authorization = %q", gotXAuth)
	}
	if gotModel != "openrouter_glm-5.2" {
		t.Errorf("x-request-model = %q", gotModel)
	}
	if gotAuth != "autoclaw" {
		t.Errorf("x-product = %q", gotAuth)
	}
}

func TestForwardFailsOverOn429(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt := strings.TrimPrefix(r.Header.Get("X-Authorization"), "Bearer ")
		seen = append(seen, jwt)
		if jwt == "good" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: ok\n\n")
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := &fakePicker{accts: []store.Account{
		{Email: "bad@x.com", AccessToken: "bad"},
		{Email: "good@x.com", AccessToken: "good"},
	}}
	f := NewForwarder(srv.URL)
	resp, used, err := f.Forward(context.Background(), p, "zai_auto", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || used.Email != "good@x.com" {
		t.Fatalf("used=%s status=%d", used.Email, resp.StatusCode)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %v", seen)
	}
}

func TestForwardSurfacesNonRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	p := &fakePicker{accts: []store.Account{{Email: "a@x.com", AccessToken: "t"}}}
	f := NewForwarder(srv.URL)
	resp, _, err := f.Forward(context.Background(), p, "zai_auto", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 surfaced as-is", resp.StatusCode)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run TestDo -v`
Expected: FAIL — `NewForwarder undefined`.

- [ ] **Step 4: Implement the forwarder**

Create `internal/proxy/upstream.go`:

```go
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
		resp, err := f.Do(ctx, acct, prefixedModel, body)
		if err == nil && !isRetryable(resp.StatusCode) {
			return resp, acct, nil // 2xx or non-retryable: surface
		}
		// Record failure and advance.
		if err != nil {
			lastErr, lastResp = err, nil
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
		resp, err := f.Do(ctx, acct, prefixedModel, body)
		if err == nil {
			return resp, acct, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("upstream forwarding failed")
	}
	return nil, acct, lastErr
}
```

> Note: the re-issue at the end keeps the surface simple and correct for tests (the exhaustion path returns a readable error response). Since `Forward` closes intermediate bodies, the returned response is always fresh.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/proxy/ ./internal/api/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/sign.go internal/proxy/upstream.go internal/proxy/upstream_test.go
git commit -m "feat(proxy): upstream forwarder with account failover (max 5)"
```

---

## Task 5: Gateway, proxy-auth middleware, config endpoints, server wiring

**Files:**
- Create: `internal/proxy/gateway.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go` (update helpers to pass a gateway)
- Test: `internal/proxy/gateway_test.go`

**Interfaces:**
- Consumes: `store.Store`, `api.BaseURL`, `NewSelector`, `NewForwarder`, `Models()`.
- Produces:
  - `type Gateway struct{...}`; `proxy.New(st store.Store, upstreamBase string) *Gateway`.
  - `(*Gateway) Register(mux *http.ServeMux)` — registers `POST /v1/chat/completions`, `POST /v1/messages`, `GET /v1/models`, `GET /api/proxy/config`, `POST /api/proxy/config`.
  - `(*Gateway) sel *Selector` and `(*Gateway) fwd *Forwarder` fields for handler files (Tasks 6, 8) — same package.
  - `(*Gateway) authOK(r *http.Request) bool` — proxy key check (accepts `Authorization: Bearer <k>` or `x-api-key: <k>`; open when key empty).
- server: `server.New(engine, refresher, st, bus, autoLogin, gw)` — new trailing `*proxy.Gateway` param; `Handler()` calls `gw.Register(mux)` when non-nil.

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/gateway_test.go`:

```go
package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogoclaw/internal/store"
)

func newGateway(t *testing.T) (*Gateway, store.Store) {
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, "http://upstream.invalid"), st
}

func serve(gw *Gateway) http.Handler {
	mux := http.NewServeMux()
	gw.Register(mux)
	return mux
}

func TestModelsEndpoint(t *testing.T) {
	gw, _ := newGateway(t)
	rr := httptest.NewRecorder()
	serve(gw).ServeHTTP(rr, httptest.NewRequest("GET", "/v1/models", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Object != "list" || len(body.Data) < 3 {
		t.Fatalf("models body = %s", rr.Body.String())
	}
}

func TestConfigRoundTrip(t *testing.T) {
	gw, _ := newGateway(t)
	h := serve(gw)

	// POST new config.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/proxy/config",
		strings.NewReader(`{"mode":"rotate_after_n","n":3,"api_key":"sk-test"}`))
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("post status %d: %s", rr.Code, rr.Body.String())
	}

	// GET reflects it; api_key is not echoed, api_key_set is true.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/proxy/config", nil))
	var got struct {
		Mode      string `json:"mode"`
		N         int    `json:"n"`
		APIKeySet bool   `json:"api_key_set"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Mode != "rotate_after_n" || got.N != 3 || !got.APIKeySet {
		t.Fatalf("config get = %+v", got)
	}
}

func TestProxyAuthRejectsWrongKey(t *testing.T) {
	gw, st := newGateway(t)
	st.SetProxyConfig(store.ProxyConfig{Mode: "sticky", N: 5, APIKey: "sk-secret"})
	h := serve(gw)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"auto"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run 'TestModels|TestConfig|TestProxyAuth' -v`
Expected: FAIL — `New undefined`.

- [ ] **Step 3: Implement the gateway**

Create `internal/proxy/gateway.go`:

```go
package proxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"gogoclaw/internal/store"
)

// Gateway is the OpenAI/Anthropic-compatible LLM proxy surface.
type Gateway struct {
	st  store.Store
	sel *Selector
	fwd *Forwarder
}

// New builds a Gateway backed by store st, forwarding to upstreamBase.
func New(st store.Store, upstreamBase string) *Gateway {
	return &Gateway{st: st, sel: NewSelector(st), fwd: NewForwarder(upstreamBase)}
}

// Register mounts all gateway routes on mux.
func (g *Gateway) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", g.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("POST /v1/messages", g.handleMessages)
	mux.HandleFunc("GET /api/proxy/config", g.handleGetConfig)
	mux.HandleFunc("POST /api/proxy/config", g.handleSetConfig)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}

// authOK enforces the proxy API key when configured. Clients may present it via
// "Authorization: Bearer <key>" (OpenAI) or "x-api-key: <key>" (Anthropic).
func (g *Gateway) authOK(r *http.Request) bool {
	cfg, err := g.st.GetProxyConfig()
	if err != nil || cfg.APIKey == "" {
		return err == nil // open when no key set; fail closed on store error
	}
	if r.Header.Get("x-api-key") == cfg.APIKey {
		return true
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return bearer == cfg.APIKey
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	var data []model
	for _, id := range Models() {
		data = append(data, model{ID: id, Object: "model", OwnedBy: "autoclaw"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := g.st.GetProxyConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	eligible := 0
	if list, err := g.sel.eligible(); err == nil {
		eligible = len(list)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": cfg.Mode, "n": cfg.N,
		"api_key_set":    cfg.APIKey != "",
		"eligible_count": eligible,
		"current":        g.sel.Current(),
	})
}

func (g *Gateway) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode   string `json:"mode"`
		N      int    `json:"n"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	switch req.Mode {
	case "sticky", "round_robin", "rotate_after_n":
	default:
		writeErr(w, http.StatusBadRequest, "mode must be sticky|round_robin|rotate_after_n")
		return
	}
	if req.N < 1 {
		req.N = 1
	}
	if err := g.st.SetProxyConfig(store.ProxyConfig{Mode: req.Mode, N: req.N, APIKey: req.APIKey}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

> Note: `handleChatCompletions` and `handleMessages` are added in Tasks 6 and 8. To compile now, add temporary stubs at the end of `gateway.go`:
>
> ```go
> func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
> 	if !g.authOK(r) {
> 		writeErr(w, http.StatusUnauthorized, "invalid api key")
> 		return
> 	}
> 	writeErr(w, http.StatusNotImplemented, "not yet")
> }
> func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
> 	if !g.authOK(r) {
> 		writeErr(w, http.StatusUnauthorized, "invalid api key")
> 		return
> 	}
> 	writeErr(w, http.StatusNotImplemented, "not yet")
> }
> ```
>
> Tasks 6 and 8 replace these stubs (delete the stub, move the handler into its own file). Keep the `authOK` guard at the top of each real handler.

- [ ] **Step 4: Wire the gateway into the server**

In `internal/server/server.go`:

Add the import `"gogoclaw/internal/proxy"`. Add a field to `Server`:

```go
	gateway   *proxy.Gateway
```

Change `New` to accept it:

```go
func New(engine *auth.AuthEngine, refresher *refresh.Refresher, st store.Store, bus *events.Bus, autoLogin AutoLogin, gateway *proxy.Gateway) *Server {
	return &Server{engine: engine, refresher: refresher, store: st, bus: bus, autoLogin: autoLogin, gateway: gateway}
}
```

In `Handler()`, before `mux.Handle("/", ...)`, add:

```go
	if s.gateway != nil {
		s.gateway.Register(mux)
	}
```

In `internal/server/server_test.go`, update both helpers `newServer` and `newServerWithAuto` to pass a gateway. In `newServer`, after `bus := events.New()`:

```go
	gw := proxy.New(st, srv.URL)
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus, nil, gw).Handler(), st
```

In `newServerWithAuto` similarly:

```go
	gw := proxy.New(st, srv.URL)
	return New(auth.New(c, st, bus), refresh.New(c, st, bus), st, bus, al, gw).Handler()
```

Add `"gogoclaw/internal/proxy"` to that test file's imports.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/proxy/ ./internal/server/ -v`
Expected: PASS (gateway config/models/auth tests + existing server tests).

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/gateway.go internal/proxy/gateway_test.go internal/server/server.go internal/server/server_test.go
git commit -m "feat(proxy): gateway routes, proxy-key auth, config endpoints; wire into server"
```

---

## Task 6: OpenAI chat-completions passthrough

**Files:**
- Create: `internal/proxy/openai.go`
- Modify: `internal/proxy/gateway.go` (remove the `handleChatCompletions` stub)
- Test: `internal/proxy/openai_test.go`

**Interfaces:**
- Consumes: `g.sel`, `g.fwd`, `Lookup`, `g.authOK`, `writeErr`, `Forwarder.Forward`.
- Produces: `(*Gateway) handleChatCompletions(w, r)` — rewrites `model`→bare, forwards, streams SSE verbatim.

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/openai_test.go`:

```go
package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogoclaw/internal/store"
	"time"
)

// gatewayTo builds a Gateway whose upstream is a caller-supplied handler, with
// one eligible account seeded.
func gatewayTo(t *testing.T, upstream http.HandlerFunc) *Gateway {
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.Add(store.Account{
		Email: "a@x.com", UserID: "1", DeviceID: "d",
		AccessToken: "JWT", RefreshToken: "R",
		AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(time.Hour),
		PrivPEM: "p", PubPEM: "P", AddedAt: time.Now(), LastRefreshedAt: time.Now(),
		Status: store.StatusActive, Balance: 100,
	})
	st.SetProxyConfig(store.ProxyConfig{Mode: "sticky", N: 5})
	return New(st, up.URL)
}

func TestChatCompletionsRewritesModelAndStreams(t *testing.T) {
	var gotModelHeader, gotBodyModel string
	gw := gatewayTo(t, func(w http.ResponseWriter, r *http.Request) {
		gotModelHeader = r.Header.Get("X-Request-Model")
		var body struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		gotBodyModel = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	})

	mux := http.NewServeMux()
	gw.Register(mux)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	mux.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if gotModelHeader != "openrouter_glm-5.2" {
		t.Errorf("X-Request-Model = %q", gotModelHeader)
	}
	if gotBodyModel != "glm-5.2" {
		t.Errorf("body model = %q, want bare glm-5.2", gotBodyModel)
	}
	if !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Errorf("stream not passed through: %s", rr.Body.String())
	}
}

func TestChatCompletionsUnknownModel(t *testing.T) {
	gw := gatewayTo(t, func(w http.ResponseWriter, r *http.Request) {})
	mux := http.NewServeMux()
	gw.Register(mux)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"mystery"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run TestChatCompletions -v`
Expected: FAIL — currently returns 501 (stub) not 200.

- [ ] **Step 3: Remove the stub and implement**

In `internal/proxy/gateway.go`, delete the temporary `handleChatCompletions` stub func.

Create `internal/proxy/openai.go`:

```go
package proxy

import (
	"encoding/json"
	"io"
	"net/http"
)

// handleChatCompletions implements POST /v1/chat/completions (OpenAI-compatible).
// It rewrites the friendly model to the bare id, sets X-Request-Model, forwards
// with account failover, and streams the SSE response through verbatim.
func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if !g.authOK(r) {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	// Preserve every field byte-exact except "model".
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var model string
	_ = json.Unmarshal(fields["model"], &model)
	route, ok := Lookup(model)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown model: "+model)
		return
	}
	fields["model"], _ = json.Marshal(route.Bare)
	body, _ := json.Marshal(fields)

	resp, _, err := g.fwd.Forward(r.Context(), g.sel, route.Prefixed(), body)
	if err == ErrNoEligible {
		writeErr(w, http.StatusServiceUnavailable, "no eligible accounts")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	streamThrough(w, resp)
}

// streamThrough copies an upstream SSE response to the client, flushing per read
// so tokens arrive incrementally.
func streamThrough(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/openai.go internal/proxy/gateway.go internal/proxy/openai_test.go
git commit -m "feat(proxy): OpenAI /v1/chat/completions passthrough with model rewrite"
```

---

## Task 7: Anthropic → OpenAI request translation

**Files:**
- Create: `internal/proxy/anthropic_request.go`
- Test: `internal/proxy/anthropic_request_test.go`

**Interfaces:**
- Produces: `translateAnthropicRequest(raw []byte) (openaiBody []byte, model string, stream bool, err error)`. Maps Anthropic Messages request → OpenAI chat-completions body (bare `model` filled by the caller; this function keeps the friendly model name in the returned `model` and sets OpenAI body `model` to the same friendly name — the caller looks it up and rewrites to bare, exactly like the OpenAI path).

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/anthropic_request_test.go`:

```go
package proxy

import (
	"encoding/json"
	"testing"
)

func TestTranslateAnthropicRequest_TextAndSystem(t *testing.T) {
	in := `{
	  "model":"auto","max_tokens":1024,"stream":true,
	  "system":"You are terse.",
	  "messages":[{"role":"user","content":"hi"}]
	}`
	out, model, stream, err := translateAnthropicRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if model != "auto" || !stream {
		t.Fatalf("model=%q stream=%v", model, stream)
	}
	var o struct {
		Model               string `json:"model"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		Stream              bool   `json:"stream"`
		Messages            []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &o); err != nil {
		t.Fatal(err)
	}
	if o.MaxCompletionTokens != 1024 || !o.Stream {
		t.Fatalf("out = %s", out)
	}
	if len(o.Messages) != 2 || o.Messages[0].Role != "system" || o.Messages[1].Role != "user" {
		t.Fatalf("messages = %s", out)
	}
}

func TestTranslateAnthropicRequest_ToolsAndToolResult(t *testing.T) {
	in := `{
	  "model":"auto","max_tokens":8,
	  "tools":[{"name":"read","description":"read file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],
	  "tool_choice":{"type":"auto"},
	  "messages":[
	    {"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"read","input":{"path":"a.txt"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"hello"}]}
	  ]
	}`
	out, _, _, err := translateAnthropicRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice any `json:"tool_choice"`
		Messages   []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
			Content    any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Tools) != 1 || o.Tools[0].Type != "function" || o.Tools[0].Function.Name != "read" {
		t.Fatalf("tools = %s", out)
	}
	if o.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %v", o.ToolChoice)
	}
	// assistant tool_use -> tool_calls with JSON-string arguments
	if len(o.Messages) != 2 || len(o.Messages[0].ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls missing: %s", out)
	}
	if o.Messages[0].ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("arguments = %q", o.Messages[0].ToolCalls[0].Function.Arguments)
	}
	// user tool_result -> role:tool with tool_call_id
	if o.Messages[1].Role != "tool" || o.Messages[1].ToolCallID != "tu_1" {
		t.Fatalf("tool result msg = %s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run TestTranslateAnthropicRequest -v`
Expected: FAIL — `translateAnthropicRequest undefined`.

- [ ] **Step 3: Implement the request translator**

Create `internal/proxy/anthropic_request.go`:

```go
package proxy

import (
	"encoding/json"
	"fmt"
)

// --- Anthropic request shapes (subset we translate) ---

type anthReq struct {
	Model      string          `json:"model"`
	System     json.RawMessage `json:"system"` // string OR []anthBlock
	Messages   []anthMessage   `json:"messages"`
	Tools      []anthTool      `json:"tools"`
	ToolChoice json.RawMessage `json:"tool_choice"`
	MaxTokens  int             `json:"max_tokens"`
	Temperature *float64       `json:"temperature"`
	Stream     bool            `json:"stream"`
}

type anthMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string OR []anthBlock
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // string OR []anthBlock (we stringify)
	// image
	Source *anthImageSource `json:"source"`
}

type anthImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// --- OpenAI target shapes ---

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Function oaiFunc `json:"function"`
}

type oaiFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// translateAnthropicRequest converts an Anthropic Messages request body into an
// OpenAI chat-completions body. The returned model is the friendly name (the
// caller resolves it via the catalog); stream reports the requested mode.
func translateAnthropicRequest(raw []byte) (out []byte, model string, stream bool, err error) {
	var in anthReq
	if err = json.Unmarshal(raw, &in); err != nil {
		return nil, "", false, fmt.Errorf("invalid anthropic request: %w", err)
	}

	msgs := make([]oaiMessage, 0, len(in.Messages)+1)

	// system → leading system message
	if len(in.System) > 0 {
		sys, serr := anthTextToString(in.System)
		if serr != nil {
			return nil, "", false, serr
		}
		if sys != "" {
			msgs = append(msgs, oaiMessage{Role: "system", Content: sys})
		}
	}

	for _, m := range in.Messages {
		translated, terr := translateMessage(m)
		if terr != nil {
			return nil, "", false, terr
		}
		msgs = append(msgs, translated...)
	}

	body := map[string]any{
		"model":    in.Model,
		"messages": msgs,
		"stream":   true, // we always stream upstream
	}
	if in.MaxTokens > 0 {
		body["max_completion_tokens"] = in.MaxTokens
	}
	if in.Temperature != nil {
		body["temperature"] = *in.Temperature
	}
	if len(in.Tools) > 0 {
		tools := make([]oaiTool, 0, len(in.Tools))
		for _, t := range in.Tools {
			var ot oaiTool
			ot.Type = "function"
			ot.Function.Name = t.Name
			ot.Function.Description = t.Description
			ot.Function.Parameters = t.InputSchema
			tools = append(tools, ot)
		}
		body["tools"] = tools
	}
	if tc := translateToolChoice(in.ToolChoice); tc != nil {
		body["tool_choice"] = tc
	}

	out, err = json.Marshal(body)
	return out, in.Model, in.Stream, err
}

// translateMessage converts one Anthropic message to one or more OpenAI messages.
func translateMessage(m anthMessage) ([]oaiMessage, error) {
	// content may be a plain string.
	var asString string
	if json.Unmarshal(m.Content, &asString) == nil {
		return []oaiMessage{{Role: m.Role, Content: asString}}, nil
	}
	var blocks []anthBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("bad content for role %s: %w", m.Role, err)
	}

	var out []oaiMessage
	var parts []any        // multimodal parts for a user/assistant text message
	var toolCalls []oaiToolCall
	var textBuf string

	flushText := func() {
		if textBuf != "" || len(parts) > 0 {
			if len(parts) > 0 {
				if textBuf != "" {
					parts = append(parts, map[string]any{"type": "text", "text": textBuf})
				}
				out = append(out, oaiMessage{Role: m.Role, Content: parts})
			} else {
				out = append(out, oaiMessage{Role: m.Role, Content: textBuf})
			}
			textBuf, parts = "", nil
		}
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			textBuf += b.Text
		case "image":
			if b.Source != nil {
				url := fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
				parts = append(parts, map[string]any{
					"type": "image_url", "image_url": map[string]string{"url": url},
				})
			}
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, oaiToolCall{
				ID: b.ID, Type: "function",
				Function: oaiFunc{Name: b.Name, Arguments: args},
			})
		case "tool_result":
			flushText()
			content, err := anthTextToString(b.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, oaiMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
		}
	}
	flushText()
	if len(toolCalls) > 0 {
		out = append(out, oaiMessage{Role: m.Role, ToolCalls: toolCalls})
	}
	if len(out) == 0 {
		out = append(out, oaiMessage{Role: m.Role, Content: ""})
	}
	return out, nil
}

// anthTextToString flattens an Anthropic string-or-blocks value to plain text.
func anthTextToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []anthBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("expected string or text blocks: %w", err)
	}
	var out string
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			out += b.Text
		}
	}
	return out, nil
}

// translateToolChoice maps Anthropic tool_choice to OpenAI's form.
func translateToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		return map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -run TestTranslateAnthropicRequest -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/anthropic_request.go internal/proxy/anthropic_request_test.go
git commit -m "feat(proxy): Anthropic->OpenAI request translation (system/tools/tool_use/tool_result)"
```

---

## Task 8: Anthropic response stream translation + /v1/messages

**Files:**
- Create: `internal/proxy/anthropic_stream.go`
- Create: `internal/proxy/testdata/oai_text_stream.txt` (from HAR entry 0)
- Create: `internal/proxy/testdata/oai_toolcall_stream.txt` (from HAR entry 2)
- Modify: `internal/proxy/gateway.go` (remove the `handleMessages` stub)
- Test: `internal/proxy/anthropic_stream_test.go`

**Interfaces:**
- Consumes: `g.sel`, `g.fwd`, `Lookup`, `translateAnthropicRequest`, `g.authOK`.
- Produces:
  - `translateOpenAIStream(w io.Writer, flush func(), r io.Reader, model string) error` — reads OpenAI SSE, writes Anthropic SSE events.
  - `(*Gateway) handleMessages(w, r)` — POST `/v1/messages`.

- [ ] **Step 1: Create test fixtures from the HAR**

Generate the two fixtures with this one-off command (run from repo root):

```bash
mkdir -p internal/proxy/testdata
python -c "
import json
har=json.load(open('llmstreamautoclaw.har','r',encoding='utf-8'))
open('internal/proxy/testdata/oai_text_stream.txt','w',encoding='utf-8').write(har['log']['entries'][0]['response']['content']['text'])
open('internal/proxy/testdata/oai_toolcall_stream.txt','w',encoding='utf-8').write(har['log']['entries'][2]['response']['content']['text'])
print('wrote fixtures')
"
```

Expected: `wrote fixtures`.

- [ ] **Step 2: Write the failing test**

Create `internal/proxy/anthropic_stream_test.go`:

```go
package proxy

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"
)

// collectEvents runs the translator over a fixture and returns the ordered list
// of Anthropic event types plus the concatenated text/thinking/tool payloads.
func runTranslate(t *testing.T, fixture string) (events []string, out string) {
	t.Helper()
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if err := translateOpenAIStream(&buf, func() {}, f, "auto"); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	sc := bufio.NewScanner(&bytes.Buffer{})
	_ = sc
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	return events, out
}

func TestTranslateTextStream(t *testing.T) {
	events, out := runTranslate(t, "testdata/oai_text_stream.txt")
	if events[0] != "message_start" {
		t.Fatalf("first event = %s", events[0])
	}
	if events[len(events)-1] != "message_stop" {
		t.Fatalf("last event = %s", events[len(events)-1])
	}
	mustContain(t, events, "content_block_start")
	mustContain(t, events, "content_block_delta")
	mustContain(t, events, "message_delta")
	// The assistant's visible answer text should survive translation.
	if !strings.Contains(out, "text_delta") {
		t.Fatalf("no text_delta events emitted")
	}
	// stop_reason end_turn for a plain text completion.
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected end_turn stop reason; out tail:\n%s", tail(out))
	}
}

func TestTranslateToolCallStream(t *testing.T) {
	_, out := runTranslate(t, "testdata/oai_toolcall_stream.txt")
	if !strings.Contains(out, `"type":"tool_use"`) {
		t.Fatalf("no tool_use content block emitted")
	}
	if !strings.Contains(out, "input_json_delta") {
		t.Fatalf("no input_json_delta events emitted")
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected tool_use stop reason; out tail:\n%s", tail(out))
	}
}

func mustContain(t *testing.T, xs []string, want string) {
	t.Helper()
	for _, x := range xs {
		if x == want {
			return
		}
	}
	t.Fatalf("events missing %q: %v", want, xs)
}

func tail(s string) string {
	if len(s) > 600 {
		return s[len(s)-600:]
	}
	return s
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run TestTranslate -v`
Expected: FAIL — `translateOpenAIStream undefined`.

- [ ] **Step 4: Implement the stream translator**

Create `internal/proxy/anthropic_stream.go`:

```go
package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// --- OpenAI streaming chunk shapes (subset) ---

type oaiChunk struct {
	ID      string      `json:"id"`
	Choices []oaiChoice `json:"choices"`
	Usage   *oaiUsage   `json:"usage"`
}

type oaiChoice struct {
	Delta        oaiDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

type oaiDelta struct {
	Content          string            `json:"content"`
	Reasoning        string            `json:"reasoning"`
	ReasoningContent string            `json:"reasoning_content"`
	ToolCalls        []oaiToolCallDelta `json:"tool_calls"`
}

type oaiToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// blockKind is the currently-open Anthropic content block type.
type blockKind int

const (
	none blockKind = iota
	textBlock
	thinkingBlock
	toolBlock
)

// streamState tracks the open Anthropic content block while translating.
type streamState struct {
	w         io.Writer
	flush     func()
	nextIndex int       // next Anthropic content-block index to assign
	openKind  blockKind // kind of the currently open block (none if closed)
	openIndex int       // index of the currently open block
	openTool  int       // OpenAI tool_calls index mapped to the open tool block
	stopReason string
	usage     oaiUsage
}

// emit writes one Anthropic SSE event.
func (s *streamState) emit(event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	s.flush()
}

func (s *streamState) closeBlock() {
	if s.openKind == none {
		return
	}
	s.emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.openIndex})
	s.openKind = none
}

func (s *streamState) openText() {
	if s.openKind == textBlock {
		return
	}
	s.closeBlock()
	s.openIndex, s.openKind = s.nextIndex, textBlock
	s.nextIndex++
	s.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.openIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (s *streamState) openThinking() {
	if s.openKind == thinkingBlock {
		return
	}
	s.closeBlock()
	s.openIndex, s.openKind = s.nextIndex, thinkingBlock
	s.nextIndex++
	s.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.openIndex,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	})
}

func (s *streamState) openTool(idx int, id, name string) {
	if s.openKind == toolBlock && s.openTool == idx {
		return
	}
	s.closeBlock()
	s.openIndex, s.openKind, s.openTool = s.nextIndex, toolBlock, idx
	s.nextIndex++
	if id == "" {
		id = fmt.Sprintf("tool_%d", idx)
	}
	s.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.openIndex,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
	})
}

// translateOpenAIStream reads an OpenAI SSE stream and writes the equivalent
// Anthropic Messages SSE event stream.
func translateOpenAIStream(w io.Writer, flush func(), r io.Reader, model string) error {
	s := &streamState{w: w, flush: flush, stopReason: "end_turn"}

	// message_start
	s.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_" + uuid(), "type": "message", "role": "assistant",
			"model": model, "content": []any{}, "stop_reason": nil,
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024) // large SSE lines
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // SSE comments (": OPENROUTER PROCESSING") and blanks
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk oaiChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // skip malformed chunk
		}
		if chunk.Usage != nil {
			s.usage = *chunk.Usage
		}
		for _, ch := range chunk.Choices {
			s.applyDelta(ch.Delta)
			if ch.FinishReason != nil {
				s.stopReason = mapStopReason(*ch.FinishReason)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	s.closeBlock()
	s.emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": s.stopReason, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": s.usage.CompletionTokens},
	})
	s.emit("message_stop", map[string]any{"type": "message_stop"})
	return nil
}

// applyDelta emits the Anthropic events for one OpenAI delta.
func (s *streamState) applyDelta(d oaiDelta) {
	// Reasoning first (thinking block).
	reason := d.Reasoning
	if reason == "" {
		reason = d.ReasoningContent
	}
	if reason != "" {
		s.openThinking()
		s.emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.openIndex,
			"delta": map[string]any{"type": "thinking_delta", "thinking": reason},
		})
	}
	if d.Content != "" {
		s.openText()
		s.emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.openIndex,
			"delta": map[string]any{"type": "text_delta", "text": d.Content},
		})
	}
	for _, tc := range d.ToolCalls {
		s.openTool(tc.Index, tc.ID, tc.Function.Name)
		if tc.Function.Arguments != "" {
			s.emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": s.openIndex,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
			})
		}
	}
}

// mapStopReason maps an OpenAI finish_reason to an Anthropic stop_reason.
func mapStopReason(fr string) string {
	switch fr {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -run TestTranslate -v`
Expected: PASS (text stream → end_turn with text_delta; tool stream → tool_use + input_json_delta).

- [ ] **Step 6: Implement the /v1/messages handler**

In `internal/proxy/gateway.go`, delete the temporary `handleMessages` stub.

Append to `internal/proxy/anthropic_stream.go`:

```go
import "net/http" // add to the existing import block; shown here for clarity

// handleMessages implements POST /v1/messages (Anthropic-compatible). It
// translates the request to OpenAI form, forwards with failover, and translates
// the streamed OpenAI response back into Anthropic SSE events.
func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
	if !g.authOK(r) {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	oaiBody, model, _, err := translateAnthropicRequest(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	route, ok := Lookup(model)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown model: "+model)
		return
	}
	// Rewrite the body's model to the bare id (translateAnthropicRequest kept the
	// friendly name).
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(oaiBody, &fields)
	fields["model"], _ = json.Marshal(route.Bare)
	oaiBody, _ = json.Marshal(fields)

	resp, _, err := g.fwd.Forward(r.Context(), g.sel, route.Prefixed(), oaiBody)
	if err == ErrNoEligible {
		writeErr(w, http.StatusServiceUnavailable, "no eligible accounts")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Surface upstream error without translation.
		streamThrough(w, resp)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := translateOpenAIStream(w, flush, resp.Body, model); err != nil {
		// Stream already started; emit an Anthropic error event.
		fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":%q}}\n\n", err.Error())
		flush()
	}
}
```

> Note: merge the `net/http` import into the file's existing import block rather than adding a second `import` statement.

- [ ] **Step 7: Add an end-to-end handler test**

Append to `internal/proxy/anthropic_stream_test.go`:

```go
func TestMessagesEndpointEndToEnd(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/oai_text_stream.txt")
	gw := gatewayTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(fixture)
	})
	mux := http.NewServeMux()
	gw.Register(mux)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "event: message_start") {
		t.Fatalf("no message_start in response:\n%s", tail(rr.Body.String()))
	}
	if !strings.Contains(rr.Body.String(), "event: message_stop") {
		t.Fatalf("no message_stop in response")
	}
}
```

Add the imports `"net/http"`, `"net/http/httptest"` to the test file (reusing `gatewayTo` from `openai_test.go`, same package).

- [ ] **Step 8: Run the full package test suite**

Run: `go test ./internal/proxy/ -v`
Expected: PASS (all proxy tests).

- [ ] **Step 9: Commit**

```bash
git add internal/proxy/anthropic_stream.go internal/proxy/anthropic_stream_test.go internal/proxy/gateway.go internal/proxy/testdata/
git commit -m "feat(proxy): Anthropic /v1/messages with OpenAI-SSE->Anthropic-SSE stream translation"
```

---

## Task 9: Dashboard gateway config panel

**Files:**
- Modify: `web/src/lib/types.ts`
- Modify: `web/src/lib/api.ts`
- Create: `web/src/hooks/useProxyConfig.ts`
- Create: `web/src/components/GatewayPanel.tsx`
- Modify: `web/src/App.tsx`
- Test: `web/src/components/GatewayPanel.test.tsx`

**Interfaces:**
- Consumes: `GET/POST /api/proxy/config`.
- Produces: `GatewayPanel` React component rendering mode select, N input (for rotate_after_n), API-key field, eligible-count/current display; `useProxyConfig()` / `useSaveProxyConfig()` hooks; `getProxyConfig()` / `saveProxyConfig()` API fns; `ProxyConfig`/`ProxyConfigView` types.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/GatewayPanel.test.tsx`:

```tsx
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider, QueryClient } from "@tanstack/react-query";
import { GatewayPanel } from "./GatewayPanel";

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

describe("GatewayPanel", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("loads config and shows the current mode", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ mode: "round_robin", n: 5, api_key_set: false, eligible_count: 2, current: "a@x.com" }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );
    render(wrap(<GatewayPanel />));
    await waitFor(() => expect(screen.getByLabelText(/rotation mode/i)).toHaveValue("round_robin"));
    expect(screen.getByText(/2 eligible/i)).toBeInTheDocument();
  });

  it("saves a new mode", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ mode: "sticky", n: 5, api_key_set: false, eligible_count: 1, current: "" }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      )
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "ok" }), { status: 200, headers: { "content-type": "application/json" } }),
      );
    render(wrap(<GatewayPanel />));
    await waitFor(() => expect(screen.getByLabelText(/rotation mode/i)).toHaveValue("sticky"));
    await userEvent.selectOptions(screen.getByLabelText(/rotation mode/i), "round_robin");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const body = JSON.parse((fetchMock.mock.calls.at(-1)?.[1] as RequestInit).body as string);
      expect(body.mode).toBe("round_robin");
    });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npx vitest run src/components/GatewayPanel.test.tsx`
Expected: FAIL — cannot resolve `./GatewayPanel`.

- [ ] **Step 3: Add types**

In `web/src/lib/types.ts`, append:

```ts
export type RotationMode = "sticky" | "round_robin" | "rotate_after_n";

export interface ProxyConfigView {
  mode: RotationMode;
  n: number;
  apiKeySet: boolean;
  eligibleCount: number;
  current: string;
}

export interface ProxyConfigUpdate {
  mode: RotationMode;
  n: number;
  apiKey: string;
}
```

- [ ] **Step 4: Add API functions**

In `web/src/lib/api.ts`, append (reusing the existing `req` helper):

```ts
import type { ProxyConfigUpdate, ProxyConfigView } from "./types";

export async function getProxyConfig(): Promise<ProxyConfigView> {
  const r = (await req("/api/proxy/config")) as {
    mode: ProxyConfigView["mode"]; n: number; api_key_set: boolean;
    eligible_count: number; current: string;
  };
  return { mode: r.mode, n: r.n, apiKeySet: r.api_key_set, eligibleCount: r.eligible_count, current: r.current };
}

export async function saveProxyConfig(c: ProxyConfigUpdate): Promise<void> {
  await req("/api/proxy/config", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ mode: c.mode, n: c.n, api_key: c.apiKey }),
  });
}
```

> Note: `web/src/lib/types.ts` already re-exports these types; adjust the top-of-file `import type` in `api.ts` if your linter prefers a single import line — merge with the existing `./types` import.

- [ ] **Step 5: Add the hooks**

Create `web/src/hooks/useProxyConfig.ts`:

```ts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getProxyConfig, saveProxyConfig } from "../lib/api";

export function useProxyConfig() {
  return useQuery({ queryKey: ["proxy-config"], queryFn: getProxyConfig });
}

export function useSaveProxyConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: saveProxyConfig,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["proxy-config"] }),
  });
}
```

- [ ] **Step 6: Build the component**

Create `web/src/components/GatewayPanel.tsx`:

```tsx
import { useEffect, useState } from "react";
import type { RotationMode } from "../lib/types";
import { useProxyConfig, useSaveProxyConfig } from "../hooks/useProxyConfig";

export function GatewayPanel() {
  const cfg = useProxyConfig();
  const save = useSaveProxyConfig();
  const [mode, setMode] = useState<RotationMode>("round_robin");
  const [n, setN] = useState(5);
  const [apiKey, setApiKey] = useState("");

  useEffect(() => {
    if (cfg.data) {
      setMode(cfg.data.mode);
      setN(cfg.data.n);
    }
  }, [cfg.data]);

  if (cfg.isLoading) {
    return <div className="h-24 animate-pulse rounded-xl border border-border bg-panel" />;
  }

  return (
    <section className="rounded-xl border border-border bg-surface p-4">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold">LLM Gateway</h2>
        <span className="text-xs text-muted">
          {cfg.data?.eligibleCount ?? 0} eligible{cfg.data?.current ? ` · active ${cfg.data.current}` : ""}
        </span>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-xs text-muted">
          Rotation mode
          <select
            aria-label="Rotation mode"
            value={mode}
            onChange={(e) => setMode(e.target.value as RotationMode)}
            className="rounded-lg border border-border bg-panel px-2 py-1.5 text-sm text-ink"
          >
            <option value="sticky">Sticky</option>
            <option value="round_robin">Round-robin</option>
            <option value="rotate_after_n">Rotate after N</option>
          </select>
        </label>

        {mode === "rotate_after_n" && (
          <label className="flex flex-col gap-1 text-xs text-muted">
            Requests per account (N)
            <input
              aria-label="Requests per account"
              type="number"
              min={1}
              value={n}
              onChange={(e) => setN(Math.max(1, Number(e.target.value)))}
              className="w-28 rounded-lg border border-border bg-panel px-2 py-1.5 text-sm text-ink"
            />
          </label>
        )}

        <label className="flex flex-col gap-1 text-xs text-muted">
          Proxy API key {cfg.data?.apiKeySet ? "(set — leave blank to keep)" : "(optional)"}
          <input
            aria-label="Proxy API key"
            type="password"
            value={apiKey}
            placeholder={cfg.data?.apiKeySet ? "••••••••" : "none"}
            onChange={(e) => setApiKey(e.target.value)}
            className="w-56 rounded-lg border border-border bg-panel px-2 py-1.5 text-sm text-ink"
          />
        </label>

        <button
          type="button"
          onClick={() => save.mutate({ mode, n, apiKey })}
          disabled={save.isPending}
          className="rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50"
        >
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>

      <p className="mt-3 text-xs text-muted">
        Point clients at <code className="text-ink">http://127.0.0.1:18432/v1</code> — OpenAI
        (<code className="text-ink">/chat/completions</code>) or Anthropic (<code className="text-ink">/messages</code>).
        Models: <code className="text-ink">glm-5.2</code>, <code className="text-ink">glm-5-turbo</code>, <code className="text-ink">auto</code>.
      </p>
    </section>
  );
}
```

> Note: the `api_key` field is write-only. Sending an empty string keeps... no — the backend overwrites with whatever is sent. To make blank "keep existing", change the save handler to omit the key when blank. Implement that by having `saveProxyConfig` send the current key only when the user typed one. Simplest correct approach: in the component, when `apiKey === ""` and `cfg.data?.apiKeySet`, the backend would clear it. Avoid that by only allowing key changes when the field is non-empty: guard the mutation — if `apiKey === ""`, re-send the sentinel by fetching current is impossible (key not returned). Therefore: treat blank as "no change" on the SERVER. Add to Task 5's `handleSetConfig`: if `req.APIKey == ""`, preserve the existing stored key. See Step 7.

- [ ] **Step 7: Make blank API key mean "keep existing" (server)**

In `internal/proxy/gateway.go` `handleSetConfig`, before saving, load the existing config and keep its key when the incoming key is blank:

```go
	key := req.APIKey
	if key == "" {
		if cur, err := g.st.GetProxyConfig(); err == nil {
			key = cur.APIKey
		}
	}
	if err := g.st.SetProxyConfig(store.ProxyConfig{Mode: req.Mode, N: req.N, APIKey: key}); err != nil {
```

Add a Go test for this in `internal/proxy/gateway_test.go`:

```go
func TestSetConfigBlankKeyKeepsExisting(t *testing.T) {
	gw, st := newGateway(t)
	st.SetProxyConfig(store.ProxyConfig{Mode: "sticky", N: 5, APIKey: "keepme"})
	h := serve(gw)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/proxy/config",
		strings.NewReader(`{"mode":"round_robin","n":2,"api_key":""}`)))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	got, _ := st.GetProxyConfig()
	if got.APIKey != "keepme" || got.Mode != "round_robin" {
		t.Fatalf("config = %+v, want key preserved + mode updated", got)
	}
}
```

Run: `go test ./internal/proxy/ -run TestSetConfig -v` → PASS.

- [ ] **Step 8: Mount the panel in the dashboard**

In `web/src/App.tsx`, import and render `GatewayPanel` below the accounts table. Add the import:

```tsx
import { GatewayPanel } from "./components/GatewayPanel";
```

And after the accounts `</div>` block (before the `{bulkOpen && ...}` line), add:

```tsx
      <div className="mt-6">
        <GatewayPanel />
      </div>
```

- [ ] **Step 9: Run the frontend tests**

Run: `cd web && npx vitest run`
Expected: PASS (GatewayPanel + existing suites).

- [ ] **Step 10: Commit**

```bash
git add web/src/lib/types.ts web/src/lib/api.ts web/src/hooks/useProxyConfig.ts web/src/components/GatewayPanel.tsx web/src/components/GatewayPanel.test.tsx web/src/App.tsx internal/proxy/gateway.go internal/proxy/gateway_test.go
git commit -m "feat(web): LLM gateway config panel (mode/N/api-key); blank key keeps existing"
```

---

## Task 10: Wire the gateway into main and build the release binary

**Files:**
- Modify: `cmd/gogoclaw/main.go`
- Modify: `cmd/gogoclaw/main_test.go` (only if it references `buildHandler`'s signature)
- Build: `web/dist` (embedded), Go binary.

**Interfaces:**
- Consumes: `proxy.New`, `api.BaseURL`.
- Produces: production wiring — the running server serves `/v1/*` backed by the real upstream.

- [ ] **Step 1: Update buildHandler to construct and pass the gateway**

In `cmd/gogoclaw/main.go`, add `"gogoclaw/internal/proxy"` to imports. Change `buildHandler`:

```go
func buildHandler(st store.Store, c *api.Client, bus *events.Bus, al server.AutoLogin) (http.Handler, *refresh.Refresher) {
	engine := auth.New(c, st, bus)
	refresher := refresh.New(c, st, bus)
	gw := proxy.New(st, api.BaseURL)
	srv := server.New(engine, refresher, st, bus, al, gw)
	return srv.Handler(), refresher
}
```

- [ ] **Step 2: Verify the whole module compiles and tests pass**

Run: `go build ./... && go test ./...`
Expected: build succeeds; all Go tests PASS. If `cmd/gogoclaw/main_test.go` calls `buildHandler`, it is unaffected (signature unchanged). If it calls `server.New` directly, add the trailing `nil` gateway argument.

- [ ] **Step 3: Build the dashboard into the embedded dist**

Run: `cd web && npm run build`
Expected: Vite writes `web/dist` (the panel is now in the bundle).

- [ ] **Step 4: Build the release binary**

Run: `go build -o gogoclaw.exe ./cmd/gogoclaw`
Expected: `gogoclaw.exe` produced, no errors.

- [ ] **Step 5: Manual smoke test (streaming end-to-end)**

Start the app: `./gogoclaw.exe` (needs at least one active account with balance in `accounts.db`).

In another shell, exercise the OpenAI path:

```bash
curl -N http://127.0.0.1:18432/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"auto","stream":true,"messages":[{"role":"user","content":"say hi in 3 words"}]}'
```

Expected: a streamed `text/event-stream` of `chat.completion.chunk` lines ending in `data: [DONE]`.

Exercise the Anthropic path:

```bash
curl -N http://127.0.0.1:18432/v1/messages \
  -H 'content-type: application/json' \
  -H 'anthropic-version: 2023-06-01' \
  -d '{"model":"auto","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"say hi in 3 words"}]}'
```

Expected: Anthropic SSE — `event: message_start` … `event: content_block_delta` (text_delta) … `event: message_stop`.

Check the models endpoint and config:

```bash
curl http://127.0.0.1:18432/v1/models
curl http://127.0.0.1:18432/api/proxy/config
```

Expected: catalog list; config JSON with `mode`, `eligible_count`.

Open `http://127.0.0.1:18432/` and confirm the LLM Gateway panel loads, lets you switch mode, and persists after reload.

- [ ] **Step 6: Commit**

```bash
git add cmd/gogoclaw/main.go gogoclaw.exe web/dist
git commit -m "feat: wire LLM stream gateway into gogoclaw server + build"
```

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** modes (Task 2), catalog (Task 3), failover ≤5 (Task 4), same-port `/v1/*` (Task 5/6/8), OpenAI passthrough (Task 6), full Anthropic translation incl. tools + thinking + streaming (Tasks 7–8), optional proxy key (Task 5/9), dashboard config (Task 9), eligibility active+balance>0 (Task 2). All spec sections map to a task.
- **Mid-stream failover limitation** is honored: `Forward` only fails over before returning the response; once `streamThrough`/`translateOpenAIStream` begins, a break surfaces to the client (Anthropic path emits an `error` event).
- **Type consistency:** `Route.Prefixed()`, `Forward(ctx, Picker, prefixedModel, body)`, `translateAnthropicRequest(raw) (body, model, stream, err)`, `translateOpenAIStream(w, flush, r, model)`, `store.ProxyConfig{Mode,N,APIKey}` are referenced consistently across tasks.
- **No placeholders:** every step contains runnable code/commands.
```
