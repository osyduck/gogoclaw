# LLM Stream Gateway — Design

**Date:** 2026-07-05
**Status:** Approved (pending spec review)

## Goal

Turn the pool of AutoClaw accounts managed by GogoClaw into a local, load-balanced
LLM endpoint. Expose OpenAI-compatible **and** Anthropic-compatible streaming APIs to
local clients (`@ai-sdk/openai-compatible`, `@ai-sdk/anthropic`) and forward each
request to the AutoClaw upstream, injecting a rotated account's JWT. The account used
per request is chosen by a configurable rotation mode: **sticky**, **rotate-after-N**,
or **round-robin**.

## Background — captured upstream behavior

Source: `llmstreamautoclaw.har` (3 real desktop requests).

Upstream endpoint (single, OpenAI/OpenRouter-shaped):

```
POST https://autoglm-api.autoglm.ai/autoclaw-proxy/proxy/autoclaw/chat/completions
```

Key request headers:

- `authorization: Bearer autoclaw-internal-proxy` — fixed internal token (constant).
- `X-Authorization: Bearer <account JWT access token>` — **per-account**, the value that rotates.
- `X-Request-Model: <prefix>_<bare>` — e.g. `openrouter_glm-5.2`, `zai_glm-5-turbo`, `zai_auto`.
  The request **body** carries the *bare* model (`"model":"glm-5.2"`), the header the prefixed id.
- Session/telemetry headers: `x_trace_id: autoclaw-desktop`, `X-Product: autoclaw`,
  `X-Version: 1.10.3`, `X-Tm: win`, `X-Channel: official`, `X-Lang: en`,
  `X-Client-Type: pc`, `X-Autoclaw-Source: desktop`, `X-Agent-Id: main`,
  `X-Session-Id`, `X-Session-Key`/`X-Autoclaw-Session-Key`, `X-Autoclaw-Agent-Id`,
  `X-Request-Id` (per-request UUIDs).

Request body: standard OpenAI chat-completions — `{model, messages, stream:true,
max_completion_tokens, tools}`. `tools` use OpenAI function shape
(`{type:"function", function:{name, description, parameters}}`).

Response: `Content-Type: text/event-stream`, chunked. Body is OpenAI/OpenRouter
streaming:

- SSE comment lines like `: OPENROUTER PROCESSING` (keepalive; pass through).
- `data: {chat.completion.chunk ...}` deltas. Text arrives in `delta.content`;
  reasoning in `delta.reasoning` + `delta.reasoning_details` (OpenRouter) or
  `delta.reasoning_content` (DeepSeek variant).
- Streaming tool calls: `delta.tool_calls: [{index, id, type:"function",
  function:{name, arguments}}]`, with `arguments` streamed as fragments across
  chunks, keyed by `index`. Multiple concurrent tool calls distinguished by `index`.
- Terminal `finish_reason`: `"stop"` (text) or `"tool_calls"`; final usage chunk
  carries `usage:{prompt_tokens, completion_tokens, total_tokens}`.
- Stream ends with `data: [DONE]`.

## Architecture

New package `internal/proxy`, mounted on the **existing** HTTP server / listener
(`127.0.0.1:18432`) — decided: same port. Clients set base URL
`http://127.0.0.1:18432/v1`.

Routes added to the server mux:

- `POST /v1/chat/completions` — OpenAI-compatible (pass-through).
- `GET  /v1/models` — model catalog.
- `POST /v1/messages` — Anthropic-compatible (translated).
- `GET  /api/proxy/config`, `POST /api/proxy/config` — read/update rotation settings.

Per-request flow:

```
client → /v1/* → [proxy auth middleware] → catalog lookup (model → prefix+bare)
       → selector.Pick() → upstream.Do(account, body) ──(retryable non-200)──▶ selector.Next(); retry ≤5
       → stream SSE back to client (verbatim for OpenAI; translated for Anthropic)
```

## Components

Each is a focused unit with a clear interface, independently testable.

### `selector.go` — rotation engine

Thread-safe. State: `mode`, `n`, integer `cursor`, per-request `count`. Reads the
live account list from `store.List()` on each pick (no stale cache).

- **Eligibility:** `status == active && balance > 0`. Ineligible accounts
  (`needs_relogin`, `refresh_failed`, zero balance) are skipped. Empty eligible
  set → selector returns a sentinel error → handler responds `503`.
- `Pick() (Account, error)` — returns the account for a *new* client request:
  - `sticky`: return the account at `cursor` (pinned); do not advance.
  - `round_robin`: advance `cursor` by 1 (mod len), return it.
  - `rotate_after_n`: increment `count`; when `count % n == 0` advance `cursor`; return account at `cursor`.
- `Next(tried map) (Account, error)` — for failover: advance to the next eligible
  account not in `tried`; error when all eligible accounts are exhausted.
- Cursor is kept stable across list changes by pinning on **email** (resolve email →
  index each pick), so add/delete/reorder doesn't scramble rotation.

### `catalog.go` — model catalog

Static, editable `map[string]ModelRoute{Prefix, Bare}`. Seeded from the HAR:

| friendly (`model` from client) | prefix       | bare          |
|--------------------------------|--------------|---------------|
| `glm-5.2`                      | `openrouter` | `glm-5.2`     |
| `glm-5-turbo`                  | `zai`        | `glm-5-turbo` |
| `auto`                         | `zai`        | `auto`        |

- `Lookup(model) (ModelRoute, ok)`. Unknown model → handler `400`.
- `GET /v1/models` returns the catalog in OpenAI `{object:"list", data:[{id,object:"model",...}]}` shape.
- Editable in one place (the seed slice) so new AutoClaw models are a one-line add.

### `upstream.go` — upstream forwarder + failover

- `Do(ctx, account, prefixedModel, bodyBytes) (*http.Response, error)` — issues the
  upstream POST with the fixed internal bearer, injected `X-Authorization`,
  `X-Request-Model`, and the full session header set (constants reused from
  `internal/api`; fresh UUIDs for `X-Request-Id`/`X-Session-Id`/`X-Session-Key` per
  attempt). Streaming client (no 30s timeout; uses request context).
- **Failover loop** (`Stream(...)`): call `selector.Pick()`, then attempt; on a
  **retryable** upstream status (`401, 403, 402, 429, 5xx`) or transport error,
  record the account in `tried`, call `selector.Next(tried)`, and retry — **max 5
  attempts total**. Non-retryable (e.g. `400`) surfaces immediately. Because bytes
  can't be un-sent, failover applies only **before the first response byte is
  streamed to the client**; a mid-stream break surfaces to the client as-is
  (documented limitation).

### `openai.go` — OpenAI-compatible handler

`/v1/chat/completions`: decode just enough to read/rewrite `model` (friendly →
bare) and look up the prefix; re-marshal body with bare model; call
`upstream.Stream`; copy the SSE stream to the client **verbatim** (comments,
reasoning, tool_calls, `[DONE]` untouched), flushing per chunk. `GET /v1/models`
serves the catalog.

### `anthropic.go` — Anthropic-compatible handler

`/v1/messages`. Bidirectional translation.

**Request (Anthropic → OpenAI):**
- `system` (string or blocks) → leading `{role:"system"}` message.
- `messages[].content` blocks:
  - `text` → string / text part.
  - `image` (base64) → OpenAI image part.
  - `tool_use` (assistant) → assistant message `tool_calls:[{id, type:"function",
    function:{name, arguments: JSON.stringify(input)}}]`.
  - `tool_result` (user) → `{role:"tool", tool_call_id, content}`.
- `tools` → OpenAI `{type:"function", function:{name, description, parameters:input_schema}}`.
- `tool_choice` mapped (`auto`/`any`→`auto`/`required`, `{type:"tool",name}`→named function).
- `max_tokens` → `max_completion_tokens`; `stream:true` forced.

**Response (OpenAI SSE → Anthropic SSE):** a small state machine emitting Anthropic
events:
- `message_start` (id, model, role, empty usage).
- One content block per active stream: **text** (from `delta.content`), **thinking**
  (from `delta.reasoning`/`reasoning_content`), **tool_use** (per `tool_calls[].index`,
  accumulating `function.arguments` fragments as `input_json_delta`).
- `content_block_start` / `content_block_delta` / `content_block_stop` bracketing each.
- `message_delta` with `stop_reason` (`stop`→`end_turn`, `tool_calls`→`tool_use`,
  `length`→`max_tokens`) and mapped `usage`.
- `message_stop`. Upstream `: OPENROUTER PROCESSING` comments and `[DONE]` are consumed
  (not forwarded). Errors mid-stream emit an Anthropic `error` event.

### `config.go` + settings persistence

- New sqlite `settings` table (key/value or a single-row typed table) storing
  `mode` (`sticky|round_robin|rotate_after_n`), `n` (int), `proxy_api_key` (string).
- `store` interface gains `GetProxyConfig()/SetProxyConfig()`.
- `GET/POST /api/proxy/config` read/update; selector reads current config (live, so a
  mode change takes effect without restart).
- Optional env/flag seeds the initial default on first run only.

### Proxy auth middleware

Wraps `/v1/*`. When `proxy_api_key` is non-empty, require the client
`Authorization: Bearer <key>` to match; respond `401` otherwise. The client
Authorization header is **not** forwarded (upstream uses the fixed internal bearer).
When the key is empty, the proxy is open (localhost bind already limits exposure).

### Dashboard UI

A "LLM Gateway" panel on the existing web dashboard:
- Mode dropdown (sticky / round-robin / rotate-after-N), N input (shown for
  rotate-after-N), proxy API key field, and the base URL + a sample model id to copy.
- Shows the currently-active / last-used account and eligible-account count.
- Persists via `POST /api/proxy/config`.

## Data model changes

- `settings` table (new). No change to `accounts` schema.
- `store.Store` interface: add `GetProxyConfig() (ProxyConfig, error)` and
  `SetProxyConfig(ProxyConfig) error`.

## Error handling

- No eligible accounts → `503 {"error":"no eligible accounts"}`.
- Unknown model → `400`.
- Missing/invalid proxy key (when configured) → `401`.
- All failover attempts exhausted → surface the last upstream status/body.
- Mid-stream upstream break → OpenAI: connection ends; Anthropic: `error` event.

## Testing

- **selector** — table-driven: each mode's advance behavior; eligibility skipping;
  `Next` failover advance + exhaustion; empty pool; email-pinned cursor stability
  across add/delete.
- **catalog** — lookup hit/miss; `/v1/models` shape.
- **upstream** — `httptest` fake upstream: failover (1st account `429` → 2nd `200`),
  non-retryable `400` surfaced, max-5 exhaustion; correct injected headers asserted.
- **openai handler** — passthrough of a canned SSE stream byte-for-byte; model
  rewrite + `X-Request-Model` set.
- **anthropic** — request translation table (text, image, tool_use, tool_result,
  tools, tool_choice); **response translation** driven by the captured HAR bodies as
  fixtures: entry 0 (text+reasoning stream) and entry 2 (streaming tool_calls),
  asserting the emitted Anthropic event sequence and final `stop_reason`.
- **config** — persistence round-trip; live mode change reflected by selector.

## Out of scope (YAGNI)

- Non-streaming responses (upstream is always streamed; if a client sends
  `stream:false` we still stream upstream and buffer into one response — minimal).
- Per-client / per-key rotation cursors (single global cursor only).
- Dynamic catalog discovery from upstream (static seed only).
- A separate proxy port (same port, decided).
