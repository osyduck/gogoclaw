# Add-Account Proxy Pool — Design

**Date:** 2026-07-05
**Status:** Approved (pending spec review)

## Problem

Adding accounts through Bulk stealth login intermittently fails with the AutoGLM
API error `630014 "Verification failed"`. The stealth browser completes the
Google consent flow (the step log reaches `consent: Continue`), so the rejection
is **not** in the browser — it happens at the Go backend's OAuth **token
exchange** call (`OAuthLogin`) to `https://autoglm-api.autoglm.ai`. AutoGLM is
flagging the server's outbound IP.

## Goal

Route the AutoGLM API calls made **while adding an account** through a dedicated,
persistent pool of HTTP proxies, and automatically fail over to the next proxy in
the pool when a call returns `630014`. This lets a flagged IP be sidestepped
without re-running the browser.

## Scope

**Proxied — only in the add-account flow, only when the batch toggle is on:**

- `OAuthURL` (called from `StartLogin`)
- `OAuthLogin` (called from `HandleCallback` — where 630014 was observed)
- The one-time `Wallets` balance seed at the end of a successful add

**Never proxied (explicitly out of scope):**

- The stealth browser (keeps using the real IP for the Google login)
- The background token refresher — `Refresh` / `Wallets` for existing accounts
  keep using the plain shared `api.Client`
- The LLM gateway (`internal/proxy`) — untouched; its "ProxyConfig" is account
  rotation, a separate concern with a separate pool

The proxy is only ever reached when a login session was started with the pool
toggle on. No other code path can pick it up.

## Design

### 1. Proxy pool storage

A new persisted single-row table holds the pool, added through the existing
additive-migration list in `internal/store/sqlite.go`:

```sql
CREATE TABLE IF NOT EXISTS login_proxy_pool (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  proxies TEXT NOT NULL          -- newline-joined proxy URLs
)
```

`store.Store` gains:

- `GetLoginProxies() ([]string, error)` — returns the list (empty slice when
  unset), splitting the stored text on newlines and dropping blanks.
- `SetLoginProxies([]string) error` — upserts the single row.

Each entry is a full proxy URL: `http://user:pass@host:port` (also `https://`
and `socks5://`). Validation on save: each non-blank line must `url.Parse`
cleanly and have both a scheme (`http`/`https`/`socks5`) and a host; an invalid
line is rejected with a message naming the offending line so the user can fix it.

### 2. API client — per-proxy variant

`api.Client` currently shares one `http.Client`. Add:

```go
func (c *Client) WithProxy(proxyURL string) (*Client, error)
```

It returns a shallow copy of the client whose `http.Client` uses
`&http.Transport{Proxy: http.ProxyURL(u)}` with the same 30s timeout. An empty
`proxyURL` returns the receiver unchanged (no proxy). A malformed URL returns an
error.

The failover trigger keys off the existing envelope error type. Export a const in
`internal/api`:

```go
const CodeVerificationFailed = 630014
```

A call is **retryable** when it fails with either:

- an `*api.APIError` whose `Code == CodeVerificationFailed`, or
- a transport error (dead/unreachable proxy).

Any other error (e.g. a different business code, a parse failure) is terminal and
stops failover immediately.

### 3. Login flow with failover

`Session` (in `internal/auth`) gains one unexported field: `order []string` — the
proxy rotation order snapshotted for this login (nil when the pool is not used).

`StartLogin` gains a `useProxyPool bool` parameter:

- When `false`: behaves exactly as today; `order` stays nil; all calls use the
  plain client.
- When `true`: it loads the pool from the store, rotates it by a batch-level
  round-robin cursor into `order` (so successive accounts in a batch start on
  different proxies), and stores `order` on the `Session`. Snapshotting at start
  time makes the login race-free against later pool edits. If the pool is empty,
  `order` is nil and the flow proceeds unproxied (the UI prevents this case by
  disabling the toggle).

A retry helper on the engine walks the order, rebuilding a proxied client per
attempt:

```go
func (e *AuthEngine) tryVia(order []string, onStep func(string), call func(*api.Client) error) error
```

- When `order` is empty it calls once with the plain client.
- Otherwise it tries each proxy in turn: `client, _ := e.api.WithProxy(p)`, then
  `call(client)`. On success it returns nil. On a retryable error it emits a
  step line and moves to the next proxy. On a terminal error it returns
  immediately. If every proxy is exhausted it returns the last error.

`tryVia` is used for all three proxied calls:

- `OAuthURL` in `StartLogin`
- `OAuthLogin` in `HandleCallback` — run through `tryVia(session.order, …)`
- the `Wallets` seed in `HandleCallback`

### 4. Progress visibility

The failover attempts emit step lines into the session's existing `Steps` slice
(the same channel the sidecar uses), so the Bulk login terminal shows failover
live. Example lines:

```
exchange via proxy #1 (proxy-a.example:8080)
630014 — trying next proxy
exchange via proxy #2 (proxy-b.example:8080)
```

`HandleCallback` currently emits no steps; it will append these through the same
locked `Steps` append the `onStep` closure uses.

### 5. Server surface

New endpoints registered in `internal/server`:

- `GET /api/login/proxy-pool` → `{ "proxies": ["http://…", …], "count": N }`.
  Returns the URLs **in plaintext** (unlike the masked gateway API key) because
  the user must see them to edit the list — acceptable for a localhost
  single-user dashboard.
- `POST /api/login/proxy-pool` with body `{ "proxies": ["http://…", …] }` →
  validates each entry, persists, returns `{ "status": "ok" }` or a 400 naming
  the bad line.

`handleBulkLogin` decodes an added `use_proxy_pool bool` from its request body and
passes it to `StartLogin`. The manual and auto single-add paths pass `false`
(bulk is the requested surface).

### 6. Frontend

- **`web/src/lib/api.ts`**: `bulkLogin(creds, provider, useProxyPool)` adds
  `use_proxy_pool` to the body. New `getLoginProxies()` / `saveLoginProxies()`.
- **`web/src/lib/types.ts`**: a `LoginProxyPool` view type.
- **New `web/src/components/LoginProxyPanel.tsx`** (mirrors `GatewayPanel`): a
  textarea, one proxy URL per line, a Save button, and a count. Backed by hooks
  `useLoginProxies` / `useSaveLoginProxies` (mirroring `useProxyConfig`). Mounted
  next to the gateway panel in `App.tsx`.
- **`web/src/components/BulkLogin.tsx`**: a checkbox *"Route AutoGLM API through
  proxy pool (N proxies)"*, disabled with an explanatory hint when the pool is
  empty. Its state threads into the `bulkLogin` call.

## Testing

- **api**: `WithProxy` sets a proxied transport (assert `Transport.Proxy` yields
  the configured URL); empty string returns the plain client; malformed URL
  errors. `CodeVerificationFailed` is surfaced as an `*APIError` by
  `decodeEnvelope`.
- **auth**: `tryVia` with an injected call func — 630014 then success advances to
  the next proxy and succeeds; all-630014 exhausts and returns the error; a
  terminal error stops after the first attempt. Round-robin cursor assigns
  distinct starting proxies across a batch. `StartLogin(useProxyPool=false)`
  leaves `order` nil and never proxies.
- **store**: `Get/SetLoginProxies` round-trip; migration creates the table on an
  older DB; blank lines dropped.
- **server**: `handleBulkLogin` forwards `use_proxy_pool`; pool GET/POST happy
  path and the 400-on-bad-line path.
- **frontend**: `BulkLogin` toggle disabled when the pool is empty and enabled
  when non-empty; the toggle value reaches `bulkLogin`. `LoginProxyPanel` loads
  and saves the list.

## Non-goals / YAGNI

- No per-account proxy syntax in the credential lines (pool + round-robin covers
  spreading across IPs).
- No proxying of the background refresher or the LLM gateway.
- No masking of pool URLs in the GET response (editability wins for a localhost
  tool).
- No health-checking / auto-pruning of dead proxies (failover already skips a
  dead proxy per login).
