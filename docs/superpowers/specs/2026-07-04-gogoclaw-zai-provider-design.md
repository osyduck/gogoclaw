# GogoClaw — Second login provider: Google via chat.z.ai

**Date:** 2026-07-04
**Status:** Approved
**Depends on:** Plans 1–4 (Go core, server/manual login, web dashboard, stealth auto-login) + credit + bulk-progress, all on `main`.

## Goal

Add a **second login provider** to GogoClaw: instead of authenticating against Google directly (`google-oauth-*`), authenticate through **chat.z.ai** as the OAuth broker (`zai-oauth-*`), which itself performs a Google sign-in. Both manual and stealth-auto (bulk) paths support the new provider. The provider is chosen with a toggle in the Bulk modal (and the Add-account flow), applying to the whole batch.

## Reference facts (from `googleviazai.har` + live Playwright capture)

The AutoClaw API exposes a provider-parallel pair, structurally identical to the Google pair:

- `POST /userapi/overseasv1/zai-oauth-url` — body `{source_id, device_id, navigate_uri}` → data `{oauth_url, state}`. `navigate_uri = http://localhost:18432/auth/callback-zai`. `oauth_url` points at `https://chat.z.ai/api/oauth/authorize?client_id=…&redirect_uri=…callback-zai&response_type=code&state=…`.
- `POST /userapi/overseasv1/zai-oauth-login` — body `{source_id, device_id, code, state, navigate_uri}` → data `{sub_id, user_id, user_name, first_login, access_token, refresh_token}`. Same `LoginResult` shape as Google plus an ignorable `sub_id`. JWT `jti` claim is still the account email; `exp` drives token expiry. Signing (`x-auth-sign`), device identity, envelope, and refresh are unchanged.

Everything downstream of the callback (JWT email extraction, account persistence, credit fetch, background refresh) is provider-agnostic and already works.

### Captured stealth flow (chat.z.ai path)

Verified end-to-end with a test account; ended at `…/auth/callback-zai?code=…&state=…`.

1. `goto(oauth_url)` → lands on `chat.z.ai/auth`.
2. Click `button "Continue with Google"` → navigates **same tab** to Google's standard sign-in. *(new pre-step)*
3. Google identifier: fill `#identifierId` → click `#identifierNext`. *(unchanged)*
4. Google password: fill `input[name="Passwd"]` → click `#passwordNext`. *(unchanged)*
5. **Account-specific** Workspace/Education speedbump → click `button "I understand"`. *(best-effort)*
6. Google OAuth consent → click approve. **Localized**: the observed button was `"Lanjutkan"` (Indonesian), not `"Continue"`. *(must be locale-robust)*
7. `chat.z.ai/auth/oauth/authorize` ("AutoGLM would like to access your Z.ai account"): **tick the ToS checkbox** (Continue is `disabled` until checked) → click `button "Continue"`. *(new post-step)*
8. Redirect → `localhost:18432/auth/callback-zai?code=…&state=…`.

Notable: the Google consent button text depends on the account's Google locale — the current driver's English-only `Continue`/`Allow` list is a latent bug for non-English accounts. chat.z.ai's own UI is English regardless.

## Design

### 1. `api` — Provider abstraction

Introduce a provider type and generalize the two OAuth calls; keep the Google-named methods as thin wrappers so existing callers are untouched.

```go
type Provider string
const (
    ProviderGoogle Provider = "google"
    ProviderZai    Provider = "zai"
)
```

- `NavigateURIFor(p Provider) string` → `http://localhost:18432/auth/callback-<p>`. (Replaces the fixed `NavigateURI` const; `callback-google` remains for Google.)
- `OAuthURL(ctx, p, deviceID) (url, state, err)` → POST `/userapi/overseasv1/<p>-oauth-url`.
- `OAuthLogin(ctx, p, deviceID, code, state) (LoginResult, err)` → POST `/userapi/overseasv1/<p>-oauth-login`.
- `GoogleOAuthURL` / `GoogleOAuthLogin` delegate with `ProviderGoogle`.
- Validation: unknown provider → error (never build an arbitrary path from user input).

### 2. `auth` — provider-aware sessions and callback

- `Session` gains an unexported `provider api.Provider`.
- `StartLogin(ctx, driver, provider, cred)` records the provider and calls `api.OAuthURL(provider, …)`.
- `HandleCallback(ctx, code, state)` reads `session.provider` and calls `api.OAuthLogin(session.provider, …)`. Idempotency/attribution behavior is unchanged.
- No new callback method needed — one handler serves both routes because `state → session → provider`.

### 3. `server` — routes and request plumbing

- Add route `GET /auth/callback-zai` → the existing `handleCallback`.
- `POST /api/login/start` and `POST /api/login/bulk` accept an optional `provider` field (`"google"` default; validated against the known set, else 400).
- Thread the provider through `StartLogin`. The `LoginDriver` interface is unchanged; the provider reaches the sidecar via the driver (below).

### 4. `sidecar` — provider-aware driver

- The `AutoDriver.Drive` payload includes `provider`. Python `drive(...)` takes `provider` (default `"google"`).
- Refactor `driver.py` into a shared Google-login helper plus provider wrappers:
  - `google`: unchanged sequence.
  - `zai`: pre-step click "Continue with Google" (`button:has-text("Google")` on `chat.z.ai/auth`), then the Google helper, then post-step — ensure the ToS checkbox is checked (`.check()`, idempotent) and click chat.z.ai `button:has-text("Continue")`.
- **Locale-robust consent** (both providers): best-effort click over `#submit_approve_access`, a multi-locale approve-text list (`Continue`, `Allow`, `Lanjutkan`, `Izinkan`, `Continuar`, `Weiter`, `Autoriser`, …), and `"I understand"` for the Workspace speedbump. Each wrapped in try/except; the flow still ends on `wait_for_url(callback)`. Anything unhandled surfaces as the per-account failure reason already shown in the UI.
- Manual (`ManualDriver`) is unaffected — the user completes whichever provider's flow in their own browser and the redirect to `/auth/callback-<provider>` finishes it.

### 5. `web` — provider toggle

- Bulk modal: a "Direct Google" / "via chat.z.ai" toggle; `bulkLogin(creds, provider)` sends `provider` in the POST body.
- Add-account (manual): the same toggle; `startLogin({provider})`.
- Per-account result rows and polling are unchanged (provider-agnostic).

## Data flow

`UI (provider) → /api/login/{start,bulk} → AuthEngine.StartLogin(provider) → api.OAuthURL(provider) → driver.Drive(oauth_url, cred)  ⟶ browser reaches /auth/callback-<provider>?code,state → handleCallback → session.provider → api.OAuthLogin(provider) → store.Add → credit/refresh (provider-agnostic).`

## Error handling

- Unknown provider at the API or server boundary → 400 / error, never a constructed path.
- Driver failures (localized consent miss, checkbox gone, wrong password, captcha) → `{ok:false, reason}` → attributed `login:error` → per-account row shows the reason (existing mechanism).
- chat.z.ai ToS checkbox uses `.check()` so a re-tick can't accidentally uncheck it.

## Testing

- `api`: `OAuthURL`/`OAuthLogin` hit the right per-provider path and `navigate_uri`; unknown provider errors; Google wrappers still pass.
- `auth`: a zai session's callback calls the zai login endpoint and persists the account; provider recorded on the session.
- `server`: `/auth/callback-zai` routed; `provider` parsed and passed through; invalid provider → 400.
- `sidecar`: zai `drive` with a fake launcher exercises pre-step + Google helper + checkbox/Continue post-step and reaches the callback; a launcher failure is non-fatal.
- `web`: toggle renders, defaults to Direct Google, and sends the chosen provider in the bulk/start body.

## Out of scope / YAGNI

- No per-line provider syntax (whole-batch toggle only).
- No provider column in the accounts table (the account is identical once logged in; provider is a login-time concern).
- No GitHub/Email login on chat.z.ai (Google only).
