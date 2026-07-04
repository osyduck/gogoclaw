# AutoClaw Account Manager (Go) — Design Spec

**Date:** 2026-07-04
**Status:** Approved (design), pending spec review
**Author:** Janu + Claude

## 1. Overview

A **local, self-hosted web dashboard** (single Go binary) to manage many AutoClaw
accounts: log in via Google OAuth and keep access tokens fresh via background
refresh. It reproduces AutoClaw's request signing and device identity so the
AutoGLM backend treats it as a legitimate client.

"Web" means a browser-based dashboard served locally — **not** a publicly hosted
multi-user service. The whole thing runs on the operator's own machine because the
OAuth callback must land on `localhost:18432` (the original app's loopback redirect).

### Goals
- Multi-account store of AutoClaw credentials (access + refresh tokens, device identity).
- **Login** (Google OAuth) with the original `localhost:18432/auth/callback-google` redirect.
  - Manual per-account login (user drives Google consent).
  - Automated bulk login (headless browser fills Google credentials).
- **Auto-refresh** access tokens before expiry, plus manual "Refresh now".
- Realtime dashboard: account list, status, token expiry, actions.

### Non-goals (explicitly out of scope for this project)
- LLM chat-completions proxy / round-robin token distribution (the Python
  reference has it; we drop it here — a separate future project if wanted).
- Public / remote hosting, multi-tenant auth, user accounts for the dashboard itself.
- Persisting Google credentials. Auto-login credentials are **one-time, in-memory,
  discarded after use** — tokens are refreshable, so re-login is rare.
- Gateway (openclaw WS) connectivity. Device identity is built authentically so this
  stays possible later, but it is not implemented now.

## 2. Reference facts (from live capture + prior RE)

Base API: `https://autoglm-api.autoglm.ai`

**Request signing** (`x-auth-sign`):
```
APP_ID   = "100003"
APP_KEY  = "38d2391985e2369a5fb8227d8e6cd5e5"   // static, hardcoded, same for all installs
ts       = floor(unixMillis/1000)
x-auth-sign = MD5(APP_ID + "&" + ts + "&" + APP_KEY)   // lowercase hex
```
The signature binds only appid+timestamp+key — **not** body/path/query/token — so it
is trivially reproducible and regenerated per request.

**Standard headers** (every request):
`x-auth-appid: 100003`, `x-auth-timestamp: <ts>`, `x-auth-sign: <md5>`,
`x-trace-id: <uuid>`, `x-version: 1.10.3`, `x-tm: win`, `x-product: autoclaw`,
`x-channel: official`, `x-lang: en`, `content-type: application/json`,
Electron UA: `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) autoclaw/1.10.3 Chrome/130.0.6723.191 Electron/33.4.11 Safari/537.36`.
Authenticated endpoints add `authorization: Bearer <access_token>`.

**device_id** (authentic derivation):
```
keypair = ed25519 (random per account)
raw32   = SPKI-DER(pubkey) minus 12-byte prefix (302a300506032b6570032100)
device_id = hex(SHA256(raw32))    // 64 hex chars
```
The Python reference uses `uuid.uuid4()` instead; we use the authentic derivation
(cheap, future-proofs gateway use, and matches server expectations if it ever validates).

**Endpoints & bodies:**
| Endpoint | Method | Auth | Body |
|---|---|---|---|
| `/userapi/overseasv1/google-oauth-url` | POST | sign only | `{source_id, device_id, navigate_uri}` |
| `/userapi/overseasv1/google-oauth-login` | POST | sign only | `{source_id, device_id, code, state, navigate_uri}` |
| `/userapi/v1/refresh` | POST | sign + Bearer | `{source_id, device_id, refresh_token}` |
| `/userapi/v1/user-profile` | POST | sign + Bearer | `{source_id, device_id}` |

Constants: `source_id = "autoclaw"`, `navigate_uri = "http://localhost:18432/auth/callback-google"`.

**Response envelope:** `{"code":0,"msg":"SUCCESS","data":{...}}`, gzip-compressed on
the wire. Do **not** set `Accept-Encoding` manually so Go's `net/http` transparently
decompresses. (The base64 in the HAR is Charles's storage format, not an API layer.)

**Token facts:** `access_token` and `refresh_token` are `Bearer <JWT>`. Claims:
`exp` (access ~24h, refresh ~30d), `jti` = the account email, `user_id`, `device_id`.
Email and expiry are read from the JWT payload (base64 decode, no verification needed).

## 3. Architecture

One process, one `http.Server` on `:18432`. A shared auth core with a **pluggable
login driver** — manual and automated login differ only in *who drives the Google
consent screen*; URL generation, callback capture, code exchange, and storage are shared.

```
Browser (dashboard, React/TanStack)
        │ HTTP (localhost:18432)
┌───────▼──────────────────────────────────────────────┐
│ Go server (single process, local)                     │
│                                                       │
│  AuthEngine ──▶ pending[state]                         │
│      ▲                │                                │
│      │           Callback listener  /auth/callback-google
│      │                │  code+state                    │
│  LoginDriver          ▼                                │
│  ├ ManualDriver   google-oauth-login ──▶ Store         │
│  └ AutoDriver(chromedp)                  ▲             │
│                                          │             │
│  Refresher (scheduler) ──────────────────┘             │
└───────────────────────────────────────────────────────┘
        │ signed HTTPS (x-auth-sign)
        ▼ autoglm-api.autoglm.ai
```

**Shared login pipeline (manual & auto):**
1. `AuthEngine.StartLogin(driver, cred)` → POST `google-oauth-url` → `{oauth_url, state}`; store `pending[state]`.
2. `LoginDriver.Drive` pushes the Google consent step (manual = user; auto = chromedp).
3. Google redirects to `localhost:18432/auth/callback-google?code&state`.
4. Callback listener matches `state`, calls `google-oauth-login` → tokens + email (`jti`).
5. Store the account; mark `pending[state]` = ok; push SSE event to dashboard.

## 4. Components & interfaces

```
autoclaw-manager/
├── cmd/manager/main.go          # wiring + start server
├── internal/
│   ├── api/       # AutoGLM client (signing + endpoints)
│   ├── identity/  # device_id (ed25519)
│   ├── store/     # SQLite (modernc.org/sqlite, pure-Go)
│   ├── auth/      # AuthEngine + LoginDriver + drivers
│   ├── refresh/   # scheduler
│   └── server/    # http handlers + callback + SSE + embed
└── web/           # Vite + React + TanStack (built → web/dist, go:embed)
```

### 4.1 `api` — AutoGLM client (stateless)
```go
type Client struct { http *http.Client; base string }

func (c *Client) signHeaders(ts int64) http.Header
func (c *Client) GoogleOAuthURL(deviceID, navURI string) (oauthURL, state string, err error)
func (c *Client) GoogleOAuthLogin(deviceID, code, state, navURI string) (LoginResult, error)
func (c *Client) Refresh(deviceID, refreshToken string) (access, refresh string, err error)
func (c *Client) UserProfile(deviceID, accessToken string) (Profile, error) // optional validation

type LoginResult struct { AccessToken, RefreshToken, UserID, UserName string; FirstLogin bool }
```
Wraps a shared `*http.Client`; every request sets signed headers; decodes the
`{code,msg,data}` envelope and returns typed `data`. Non-zero `code` → error.

### 4.2 `identity` — device_id
```go
type Identity struct { DeviceID, PubPEM, PrivPEM string }
func New() (Identity, error)  // ed25519 keypair → SHA256(raw32 pubkey) hex
```
Generated once per account at first login, persisted in the store.

### 4.3 `store` — SQLite (interface + impl)
```go
type Account struct {
    Email, UserID, DeviceID           string
    AccessToken, RefreshToken         string
    AccessExpiresAt, RefreshExpiresAt time.Time
    PrivPEM, PubPEM                   string
    AddedAt, LastRefreshedAt          time.Time
    Status                            string // active | refresh_failed | needs_relogin
}
type Store interface {
    Add(Account) error
    List() ([]Account, error)
    Get(email string) (Account, error)
    UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error
    SetStatus(email, status string) error
    Delete(email string) error
}
```
Schema: one `accounts` table, `email` primary key. `modernc.org/sqlite` (pure Go,
no cgo) → single static binary. DB file `accounts.db` beside the binary.

### 4.4 `auth` — AuthEngine + drivers
```go
type GoogleCred struct { Email, Password string } // in-memory only, never persisted

type LoginDriver interface {
    Drive(ctx context.Context, oauthURL string, cred *GoogleCred) error
}
type Session struct { State, Status string; Account *Account; Err error } // pending|ok|error

type AuthEngine struct { api *api.Client; store store.Store; pending map[string]*Session; mu sync.Mutex }
func (e *AuthEngine) StartLogin(driver LoginDriver, cred *GoogleCred) (state string, err error)
func (e *AuthEngine) HandleCallback(code, state string) error
func (e *AuthEngine) Status(state string) Session
```
- **ManualDriver**: returns the `oauth_url` for the UI to open; does not block —
  the callback completes the flow.
- **AutoDriver**: chromedp headless — `#identifierId` → `#identifierNext` →
  `input[name="Passwd"]` → `#passwordNext` → click consent (allow/continue/accept…),
  wait for redirect to `localhost:18432`. Bounded concurrency (2–3) for bulk.

### 4.5 `refresh` — scheduler
```go
func (r *Refresher) Run(ctx context.Context)          // tick 1m: refresh where AccessExpiresAt - 5m has passed
func (r *Refresher) RefreshOne(email string) error    // manual "Refresh now"
func (r *Refresher) RefreshAll() error
```
On refresh failure (e.g. refresh_token expired) → `SetStatus(needs_relogin)`; surfaced in UI.

### 4.6 `server` — HTTP (single listener `:18432`)
| Route | Purpose |
|---|---|
| `GET /` and static | React dashboard (go:embed `web/dist`) |
| `GET /auth/callback-google` | OAuth callback → `HandleCallback` → tiny HTML "close this tab" |
| `POST /api/login/start` | `{mode: manual\|auto, cred?}` → `{state, oauth_url?}` |
| `GET /api/login/status?state=` | poll fallback for a session |
| `GET /api/accounts` | list accounts |
| `POST /api/accounts/{email}/refresh` | refresh one |
| `POST /api/accounts/refresh-all` | refresh all |
| `DELETE /api/accounts/{email}` | remove |
| `GET /api/events` | **SSE** — push login-status + refresh events |

## 5. Login flows

**Manual:** UI `[+ Add]` → `POST /api/login/start {mode:manual}` → `{state, oauth_url}`
→ UI opens `oauth_url` in a new tab → user logs in → Google → `/auth/callback-google`
→ exchange + store → SSE `login:ok` → dashboard refetches; callback tab shows a
"Login successful — you can close this tab" page.

**Auto (bulk):** UI `[Bulk login]` → textarea of `email:password` lines →
per line `POST /api/login/start {mode:auto, cred}` → AutoDriver (chromedp) drives
consent → same callback → SSE progress per state. Credentials used in-memory, then
discarded. Concurrency capped; per-account failures reported without aborting the batch.

## 6. Web UI (TanStack) & design direction

**Stack:** Vite + React + TypeScript, **TanStack Query** (fetch/cache/auto-refetch),
**TanStack Table** (sortable/filterable account table), Tailwind. `vite build` →
`web/dist` → `go:embed` → served at `:18432/`. Single binary output; Node toolchain
is dev-only. Realtime: one SSE connection; on `login:*` / `refresh:*` events call
`queryClient.invalidateQueries(['accounts'])`.

**Design register:** product/tool (design serves the task; earned familiarity, the
tool disappears into the work — Linear/Raycast/Stripe bar).

**Direction (committed):**
- **Theme:** dark, operational cockpit. Physical scene: a power user monitoring many
  accounts' token health at a glance in a focused session. Pure-neutral dark surfaces
  (chroma 0), two neutral layers (content vs. toolbar/sidebar).
- **Color strategy:** Restrained. Brand anchor **plum `oklch(0.360 0.147 340)`**
  (primary actions, current selection, focus ring only — not decoration). This
  distinctive plum is the identity move that keeps it off the navy-SaaS / green-terminal
  reflex lanes.
- **Semantic status vocabulary:** `active` = green, `needs_relogin` = amber,
  `refresh_failed` = red — used for the status dot/badge and nothing decorative.
  All must clear contrast (body ≥4.5:1, large ≥3:1) on the dark surface.
- **Typography:** one well-tuned sans (Inter / system-ui). Fixed rem scale
  (~1.2 ratio), not fluid. Tabular-nums for token-expiry countdowns.
- **Components:** every interactive element ships default/hover/focus/active/
  disabled/loading. Skeleton rows while loading (no center spinner). Empty state
  teaches ("Add your first account" with the two flows), not "nothing here".
  Standard affordances only — no invented controls, modal is a last resort.
- **Motion:** 150–250ms, state-only (row status change, refresh pulse, toast).
  No page-load choreography. `prefers-reduced-motion` alternative required.

Detailed crafting (exact tokens, component states, browser-tested contrast) happens
during implementation via the `impeccable` skill; this section fixes the direction.

## 7. Security & operational notes
- Runs on loopback only; bind `127.0.0.1:18432` (not `0.0.0.0`).
- Google credentials never touch disk; only transit AutoDriver in memory.
- Token store (`accounts.db`) holds live bearer tokens — it is sensitive; file lives
  beside the binary with default user-only perms. (Encryption-at-rest = future option.)
- `x-auth-sign` freshness: regenerate timestamp+sign per request; the server enforces
  a timestamp window.
- Cannot run alongside the real AutoClaw app (port 18432 conflict) — it is a replacement.

## 8. Error handling & edge cases
- Callback with unknown/expired `state` → 400 + friendly HTML; no store write.
- `google-oauth-login` non-zero `code` → session `error`, surfaced via SSE.
- Duplicate account (same email) → update existing tokens/identity, don't duplicate rows.
- Refresh: `refresh` returns new access (+ maybe new refresh); persist both, update `LastRefreshedAt`.
- Refresh failure → `needs_relogin`; scheduler skips until re-login.
- chromedp: Chrome not installed → clear error on the auto flow only (manual unaffected).
- Bulk: bounded concurrency; one failure doesn't abort the batch; per-line result reported.

## 9. Testing strategy
- `api`: unit-test `signHeaders` against the known vector
  (`MD5("100003&1783104511&38d2391985e2369a5fb8227d8e6cd5e5") = 711fcef3...`); JWT
  claim parsing; envelope decode incl. gzip.
- `identity`: device_id derivation against the known value
  (`ed76ed103eae695f5549e2ff78ca3cfeb67e2d9ede690c7c6c50ce1fd6416565` from its keypair).
- `store`: CRUD + status transitions (in-memory SQLite).
- `auth`: `HandleCallback` state matching with a mocked `api.Client`.
- `refresh`: due-selection logic with fake clock.
- End-to-end manual login smoke test against the live API (one real account).

## 10. Tech stack summary
- Go stdlib `net/http`; `modernc.org/sqlite` (pure Go); `chromedp` (auto driver only).
- Frontend: Vite + React + TS + TanStack Query/Table + Tailwind, embedded via `go:embed`.
- Build: `vite build` then `go build` → one static binary + `accounts.db`.

## 11. Open questions / future
- Encrypt `accounts.db` at rest?
- Re-introduce the LLM proxy (chat completions, round-robin) as a follow-up project?
- Gateway (openclaw WS) connectivity using the stored ed25519 identity?
- Light theme variant for the dashboard?
