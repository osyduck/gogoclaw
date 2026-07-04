# GogoClaw — Plan 3: Web Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Plan 2 placeholder UI with a real React + TanStack dashboard that lists accounts, shows live status/token-expiry, starts manual logins, and refreshes/deletes accounts — built in `web/`, compiled to `web/dist`, and embedded by the existing `web.go`.

**Architecture:** A Vite + React + TypeScript SPA under `web/` consuming the Plan 2 REST API and SSE stream on the same origin (`:18432`). TanStack Query owns server state (accounts list, mutations); a single SSE connection invalidates the accounts query on `login:*`/`refresh:*`/`account:deleted` events. TanStack Table (v8 stable) renders the sortable account table. Tailwind v4 (via the `@tailwindcss/vite` plugin) + Phosphor icons implement a dark operational-cockpit theme with a plum brand accent. `npm run build` outputs to `web/dist`, which `web.web.go` already embeds — so the Go binary keeps serving the UI unchanged.

**Tech Stack (all package names/APIs verified via context7):** Vite 6 + React 19 + TypeScript, `@tanstack/react-query` (v5), `@tanstack/react-table` (v8 stable — `useReactTable`/`flexRender`, NOT the v9 `@beta` `useTable`), `tailwindcss` + `@tailwindcss/vite` (v4), `@phosphor-icons/react`. Tests: Vitest + `@testing-library/react` + jsdom.

## Global Constraints

- Lives in `web/`; the Go embed contract is unchanged: `web/web.go` does `//go:embed dist` and `DistFS()` returns it rooted at `dist`. `npm run build` (Vite) must output to `web/dist`.
- `web/dist` is committed (built output) so `go build ./...` works without Node — preserves the single-binary story. `web/node_modules` is git-ignored.
- Same-origin in production (served from `:18432`); dev uses a Vite proxy for `/api` and `/auth` → `http://127.0.0.1:18432`.
- REST contract (from Plan 2, verbatim): `GET /api/accounts` → array of `{email, user_id, status, access_expires_at, refresh_expires_at, last_refreshed_at, added_at}` (unix seconds; NO tokens). `POST /api/login/start` body `{"mode":"manual"}` → `{state, oauth_url}` (mode `auto` → 501). `GET /api/login/status?state=` → `{state,status,email?,error?,created_at}` (status `pending|ok|error`). `POST /api/accounts/{email}/refresh` → `{status}` (404 if missing). `POST /api/accounts/refresh-all`. `DELETE /api/accounts/{email}`. `GET /api/events` → SSE `data: {"type","email?","detail?"}` (types `login:ok|login:error|refresh:ok|refresh:failed|account:deleted`).
- Account status values: `active` | `refresh_failed` | `needs_relogin`.
- Design register: product/tool, dark cockpit. Brand anchor plum `oklch(0.360 0.147 340)` (lightened for dark-mode interactive contrast). Semantic status colors: active=green, needs_relogin=amber, refresh_failed=red. One sans (Inter/system), fixed rem scale, tabular-nums for countdowns. Every interactive element ships default/hover/focus/active/disabled; skeleton loading (no center spinner); empty state teaches the two login flows; motion 150–250ms with `prefers-reduced-motion` fallback.
- Bulk/auto login is Plan 4 — its UI entry point renders **disabled** with a "coming in Plan 4" hint; do not wire it.

## Plan roadmap (this is Plan 3 of 4)

1. Plan 1 — Go core ✅. 2. Plan 2 — Server + manual login ✅. 3. **Plan 3 — Web dashboard (this doc).** 4. Plan 4 — Stealth auto-login (Python CloakBrowser sidecar + AutoDriver + bulk UI).

---

### Task 1: Vite + Tailwind + Vitest scaffold and build pipeline

**Files:**
- Create: `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/tsconfig.node.json`, `web/index.html`, `web/vitest.setup.ts`
- Create: `web/src/main.tsx`, `web/src/index.css`, `web/src/App.tsx`, `web/src/App.test.tsx`
- Modify: `.gitignore` (ignore `web/node_modules`)

**Interfaces:**
- Consumes: nothing.
- Produces: a working Vite project — `npm run build` emits `web/dist/index.html` + `web/dist/assets/*`; `npm test` runs Vitest; `App` renders a titled shell. The Tailwind design tokens (CSS `@theme`) that later tasks style against.

- [ ] **Step 1: Create the project config files**

Create `web/package.json`:
```json
{
  "name": "gogoclaw-web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview",
    "test": "vitest run"
  },
  "dependencies": {
    "@phosphor-icons/react": "^2",
    "@tanstack/react-query": "^5",
    "@tanstack/react-table": "^8",
    "react": "^19",
    "react-dom": "^19"
  },
  "devDependencies": {
    "@tailwindcss/vite": "^4",
    "@testing-library/jest-dom": "^6",
    "@testing-library/react": "^16",
    "@testing-library/user-event": "^14",
    "@types/react": "^19",
    "@types/react-dom": "^19",
    "@vitejs/plugin-react": "^4",
    "jsdom": "^25",
    "tailwindcss": "^4",
    "typescript": "^5",
    "vite": "^6",
    "vitest": "^2"
  }
}
```

Create `web/vite.config.ts`:
```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:18432", changeOrigin: true },
      "/auth": { target: "http://127.0.0.1:18432", changeOrigin: true },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
  },
});
```

Create `web/tsconfig.json`:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "useDefineForClassFields": true,
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "resolveJsonModule": true,
    "isolatedModules": true,
    "moduleDetection": "force",
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "types": ["vitest/globals", "@testing-library/jest-dom"]
  },
  "include": ["src", "vitest.setup.ts"],
  "references": [{ "path": "./tsconfig.node.json" }]
}
```

Create `web/tsconfig.node.json`:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2023"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowSyntheticDefaultImports": true,
    "strict": true,
    "noEmit": true
  },
  "include": ["vite.config.ts"]
}
```

Create `web/index.html`:
```html
<!doctype html>
<html lang="en" class="dark">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>GogoClaw</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

Create `web/vitest.setup.ts`:
```ts
import "@testing-library/jest-dom/vitest";
```

- [ ] **Step 2: Create the app entry, theme, and a smoke test**

Create `web/src/index.css`:
```css
@import "tailwindcss";

@theme {
  --color-bg: oklch(0.16 0 0);
  --color-surface: oklch(0.20 0 0);
  --color-panel: oklch(0.24 0 0);
  --color-border: oklch(0.30 0 0);
  --color-ink: oklch(0.96 0 0);
  --color-muted: oklch(0.72 0 0);
  --color-brand: oklch(0.62 0.16 340);
  --color-brand-ink: oklch(0.98 0.02 340);
  --color-ok: oklch(0.74 0.15 150);
  --color-warn: oklch(0.82 0.13 85);
  --color-err: oklch(0.68 0.20 25);
  --font-sans: "Inter", ui-sans-serif, system-ui, sans-serif;
}

html, body, #root { height: 100%; }
body { background: var(--color-bg); color: var(--color-ink); font-family: var(--font-sans); }
```

Create `web/src/main.tsx`:
```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./App";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
```

Create `web/src/App.tsx`:
```tsx
export function App() {
  return (
    <div className="min-h-full">
      <h1 className="p-6 text-2xl font-semibold">GogoClaw</h1>
    </div>
  );
}
```

Create `web/src/App.test.tsx`:
```tsx
import { render, screen } from "@testing-library/react";
import { App } from "./App";

test("renders the app title", () => {
  render(<App />);
  expect(screen.getByRole("heading", { name: /gogoclaw/i })).toBeInTheDocument();
});
```

- [ ] **Step 3: Ignore node_modules**

Add to the repo-root `.gitignore` (append):
```
# Frontend deps
web/node_modules/
```

- [ ] **Step 4: Install, test, build**

Run:
```bash
cd /e/autoclaw/web && npm install && npm test && npm run build
```
Expected: `npm install` succeeds; `npm test` shows 1 passing test; `npm run build` writes `web/dist/index.html` and `web/dist/assets/*`. Verify `ls dist` shows `index.html` and `assets/`.

- [ ] **Step 5: Verify the Go embed still builds against the real dist**

Run:
```bash
cd /e/autoclaw && go build ./... && go test ./internal/server/ -run TestServesPlaceholderUI -count=1
```
Note: `TestServesPlaceholderUI` asserts the served HTML contains "GogoClaw" — the Vite `index.html` still has `<title>GogoClaw</title>`, so it passes. Expected: build ok, test PASS.

- [ ] **Step 6: Commit (including the built dist)**

```bash
cd /e/autoclaw
git add .gitignore web/package.json web/package-lock.json web/vite.config.ts web/tsconfig.json web/tsconfig.node.json web/index.html web/vitest.setup.ts web/src web/dist
git commit -m "feat(web): Vite+React+Tailwind+Vitest scaffold, build to web/dist"
```

---

### Task 2: Types, API client, QueryClient

**Files:**
- Create: `web/src/lib/types.ts`, `web/src/lib/api.ts`, `web/src/lib/queryClient.ts`
- Test: `web/src/lib/api.test.ts`

**Interfaces:**
- Consumes: Task 1 scaffold.
- Produces:
  - `types.ts`: `Account` (`email,userId,status,accessExpiresAt,refreshExpiresAt,lastRefreshedAt,addedAt`; status `AccountStatus = "active"|"refresh_failed"|"needs_relogin"`), `LoginStart` (`state,oauthUrl`), `LoginSession` (`state,status,email?,error?`), `GogoEvent` (`type,email?,detail?`).
  - `api.ts`: `listAccounts()`, `startManualLogin()`, `loginStatus(state)`, `refreshAccount(email)`, `refreshAll()`, `deleteAccount(email)` — all `async`, throwing on non-2xx.
  - `queryClient.ts`: a configured `QueryClient` instance.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/api.test.ts`:
```ts
import { afterEach, expect, test, vi } from "vitest";
import { listAccounts, startManualLogin, refreshAccount } from "./api";

afterEach(() => vi.restoreAllMocks());

function mockFetch(status: number, body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), {
    status, headers: { "content-type": "application/json" },
  })));
}

test("listAccounts maps snake_case fields to the Account shape", async () => {
  mockFetch(200, [{
    email: "a@x.com", user_id: "u1", status: "active",
    access_expires_at: 111, refresh_expires_at: 222, last_refreshed_at: 333, added_at: 444,
  }]);
  const accts = await listAccounts();
  expect(accts).toHaveLength(1);
  expect(accts[0]).toEqual({
    email: "a@x.com", userId: "u1", status: "active",
    accessExpiresAt: 111, refreshExpiresAt: 222, lastRefreshedAt: 333, addedAt: 444,
  });
});

test("startManualLogin returns state + oauthUrl", async () => {
  mockFetch(200, { state: "st1", oauth_url: "https://g/o" });
  const r = await startManualLogin();
  expect(r).toEqual({ state: "st1", oauthUrl: "https://g/o" });
});

test("refreshAccount throws on a 404", async () => {
  mockFetch(404, { error: "account not found" });
  await expect(refreshAccount("missing@x.com")).rejects.toThrow(/account not found/);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /e/autoclaw/web && npx vitest run src/lib/api.test.ts`
Expected: FAIL — cannot resolve `./api`.

- [ ] **Step 3: Write the implementations**

Create `web/src/lib/types.ts`:
```ts
export type AccountStatus = "active" | "refresh_failed" | "needs_relogin";

export interface Account {
  email: string;
  userId: string;
  status: AccountStatus;
  accessExpiresAt: number;
  refreshExpiresAt: number;
  lastRefreshedAt: number;
  addedAt: number;
}

export interface LoginStart {
  state: string;
  oauthUrl: string;
}

export interface LoginSession {
  state: string;
  status: "pending" | "ok" | "error";
  email?: string;
  error?: string;
}

export interface GogoEvent {
  type: string;
  email?: string;
  detail?: string;
}
```

Create `web/src/lib/api.ts`:
```ts
import type { Account, LoginSession, LoginStart } from "./types";

async function req(path: string, init?: RequestInit): Promise<unknown> {
  const res = await fetch(path, init);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    const msg = (body as { error?: string }).error ?? `request failed (${res.status})`;
    throw new Error(msg);
  }
  return body;
}

interface RawAccount {
  email: string; user_id: string; status: Account["status"];
  access_expires_at: number; refresh_expires_at: number;
  last_refreshed_at: number; added_at: number;
}

export async function listAccounts(): Promise<Account[]> {
  const raw = (await req("/api/accounts")) as RawAccount[];
  return raw.map((a) => ({
    email: a.email, userId: a.user_id, status: a.status,
    accessExpiresAt: a.access_expires_at, refreshExpiresAt: a.refresh_expires_at,
    lastRefreshedAt: a.last_refreshed_at, addedAt: a.added_at,
  }));
}

export async function startManualLogin(): Promise<LoginStart> {
  const r = (await req("/api/login/start", {
    method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({ mode: "manual" }),
  })) as { state: string; oauth_url: string };
  return { state: r.state, oauthUrl: r.oauth_url };
}

export async function loginStatus(state: string): Promise<LoginSession> {
  return (await req(`/api/login/status?state=${encodeURIComponent(state)}`)) as LoginSession;
}

export async function refreshAccount(email: string): Promise<void> {
  await req(`/api/accounts/${encodeURIComponent(email)}/refresh`, { method: "POST" });
}

export async function refreshAll(): Promise<void> {
  await req("/api/accounts/refresh-all", { method: "POST" });
}

export async function deleteAccount(email: string): Promise<void> {
  await req(`/api/accounts/${encodeURIComponent(email)}`, { method: "DELETE" });
}
```

Create `web/src/lib/queryClient.ts`:
```ts
import { QueryClient } from "@tanstack/react-query";

export const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 5_000, retry: 1 } },
});
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /e/autoclaw/web && npx vitest run src/lib/api.test.ts`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
cd /e/autoclaw && git add web/src/lib && git commit -m "feat(web): API client, types, and QueryClient"
```

---

### Task 3: Account query hooks + SSE invalidation

**Files:**
- Create: `web/src/hooks/useAccounts.ts`, `web/src/hooks/useServerEvents.ts`
- Test: `web/src/hooks/useServerEvents.test.tsx`

**Interfaces:**
- Consumes: `api.ts`, `queryClient.ts`.
- Produces:
  - `useAccounts()` → TanStack Query result for the accounts list (queryKey `["accounts"]`).
  - `useRefreshAccount()`, `useRefreshAll()`, `useDeleteAccount()` → mutations that invalidate `["accounts"]` on success.
  - `useServerEvents()` → opens one `EventSource("/api/events")` and invalidates `["accounts"]` on every message; cleans up on unmount.

- [ ] **Step 1: Write the failing test**

Create `web/src/hooks/useServerEvents.test.tsx`:
```tsx
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { useServerEvents } from "./useServerEvents";

class FakeEventSource {
  static last: FakeEventSource | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  closed = false;
  constructor(public url: string) { FakeEventSource.last = this; }
  emit(data: unknown) { this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent); }
  close() { this.closed = true; }
}

afterEach(() => vi.restoreAllMocks());

function Harness() {
  useServerEvents();
  return null;
}

test("an SSE message invalidates the accounts query", async () => {
  vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);
  const qc = new QueryClient();
  const spy = vi.spyOn(qc, "invalidateQueries");
  const { unmount } = render(
    <QueryClientProvider client={qc}><Harness /></QueryClientProvider>,
  );
  FakeEventSource.last!.emit({ type: "refresh:ok", email: "a@x.com" });
  await waitFor(() => expect(spy).toHaveBeenCalledWith({ queryKey: ["accounts"] }));
  unmount();
  expect(FakeEventSource.last!.closed).toBe(true);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /e/autoclaw/web && npx vitest run src/hooks/useServerEvents.test.tsx`
Expected: FAIL — cannot resolve `./useServerEvents`.

- [ ] **Step 3: Write the hooks**

Create `web/src/hooks/useAccounts.ts`:
```ts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { deleteAccount, listAccounts, refreshAccount, refreshAll } from "../lib/api";

export function useAccounts() {
  return useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
}

function useAccountsInvalidator() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: ["accounts"] });
}

export function useRefreshAccount() {
  const invalidate = useAccountsInvalidator();
  return useMutation({ mutationFn: refreshAccount, onSuccess: invalidate });
}

export function useRefreshAll() {
  const invalidate = useAccountsInvalidator();
  return useMutation({ mutationFn: refreshAll, onSuccess: invalidate });
}

export function useDeleteAccount() {
  const invalidate = useAccountsInvalidator();
  return useMutation({ mutationFn: deleteAccount, onSuccess: invalidate });
}
```

Create `web/src/hooks/useServerEvents.ts`:
```ts
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";

// useServerEvents opens the SSE stream and refetches the accounts list whenever
// the server reports a login/refresh/delete event.
export function useServerEvents() {
  const qc = useQueryClient();
  useEffect(() => {
    const es = new EventSource("/api/events");
    es.onmessage = () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
    };
    return () => es.close();
  }, [qc]);
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /e/autoclaw/web && npx vitest run src/hooks/useServerEvents.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /e/autoclaw && git add web/src/hooks && git commit -m "feat(web): accounts query/mutation hooks + SSE invalidation"
```

---

### Task 4: Status badge, expiry countdown, and the accounts table

**Files:**
- Create: `web/src/components/StatusBadge.tsx`, `web/src/components/Expiry.tsx`, `web/src/components/AccountsTable.tsx`
- Test: `web/src/components/AccountsTable.test.tsx`, `web/src/components/Expiry.test.tsx`

**Interfaces:**
- Consumes: `types.ts` (`Account`, `AccountStatus`).
- Produces:
  - `StatusBadge({ status })` — a dot+label styled per status (green/amber/red).
  - `formatExpiry(unixSeconds, now?)` (exported) + `Expiry({ at })` — relative countdown ("23h 41m", "expired", "—" for 0).
  - `AccountsTable({ accounts, onRefresh, onDelete, busyEmail? })` — a TanStack Table v8 table (`useReactTable` + `getCoreRowModel` + `getSortedRowModel` + `flexRender`) with columns Email, Status, Access expiry, Last refresh, Actions (Refresh / Delete buttons calling the callbacks).

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/Expiry.test.tsx`:
```tsx
import { expect, test } from "vitest";
import { formatExpiry } from "./Expiry";

test("formatExpiry shows hours+minutes in the future", () => {
  const now = 1_000_000;
  expect(formatExpiry(now + 3600 + 41 * 60, now)).toBe("1h 41m");
});

test("formatExpiry shows days+hours beyond a day", () => {
  const now = 1_000_000;
  expect(formatExpiry(now + 25 * 3600, now)).toBe("1d 1h");
});

test("formatExpiry shows 'expired' in the past", () => {
  const now = 1_000_000;
  expect(formatExpiry(now - 10, now)).toBe("expired");
});

test("formatExpiry shows an em dash for a zero timestamp", () => {
  expect(formatExpiry(0, 1_000_000)).toBe("—");
});
```

Create `web/src/components/AccountsTable.test.tsx`:
```tsx
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import type { Account } from "../lib/types";
import { AccountsTable } from "./AccountsTable";

const accts: Account[] = [
  { email: "a@x.com", userId: "u1", status: "active", accessExpiresAt: 9_999_999_999, refreshExpiresAt: 9_999_999_999, lastRefreshedAt: 1, addedAt: 1 },
  { email: "b@x.com", userId: "u2", status: "needs_relogin", accessExpiresAt: 0, refreshExpiresAt: 0, lastRefreshedAt: 1, addedAt: 1 },
];

test("renders a row per account with the email and status", () => {
  render(<AccountsTable accounts={accts} onRefresh={() => {}} onDelete={() => {}} />);
  expect(screen.getByText("a@x.com")).toBeInTheDocument();
  expect(screen.getByText("b@x.com")).toBeInTheDocument();
  expect(screen.getByText(/needs re-?login/i)).toBeInTheDocument();
});

test("clicking Refresh fires onRefresh with the row email", async () => {
  const onRefresh = vi.fn();
  render(<AccountsTable accounts={accts} onRefresh={onRefresh} onDelete={() => {}} />);
  const rows = screen.getAllByRole("row");
  // header row + 2 data rows; click the first data row's Refresh button
  const firstRowRefresh = within(rows[1]).getByRole("button", { name: /refresh/i });
  await userEvent.click(firstRowRefresh);
  expect(onRefresh).toHaveBeenCalledWith("a@x.com");
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /e/autoclaw/web && npx vitest run src/components/`
Expected: FAIL — cannot resolve the component modules.

- [ ] **Step 3: Write the components**

Create `web/src/components/StatusBadge.tsx`:
```tsx
import type { AccountStatus } from "../lib/types";

const META: Record<AccountStatus, { label: string; color: string }> = {
  active: { label: "Active", color: "var(--color-ok)" },
  refresh_failed: { label: "Refresh failed", color: "var(--color-err)" },
  needs_relogin: { label: "Needs re-login", color: "var(--color-warn)" },
};

export function StatusBadge({ status }: { status: AccountStatus }) {
  const m = META[status];
  return (
    <span className="inline-flex items-center gap-2 text-sm text-muted">
      <span className="size-2 rounded-full" style={{ background: m.color }} aria-hidden />
      {m.label}
    </span>
  );
}
```

Create `web/src/components/Expiry.tsx`:
```tsx
// formatExpiry renders a compact relative time-to-expiry. 0 => em dash; past => "expired".
export function formatExpiry(atUnixSeconds: number, nowUnixSeconds = Math.floor(Date.now() / 1000)): string {
  if (atUnixSeconds === 0) return "—";
  const delta = atUnixSeconds - nowUnixSeconds;
  if (delta <= 0) return "expired";
  const h = Math.floor(delta / 3600);
  const m = Math.floor((delta % 3600) / 60);
  if (h >= 24) {
    const d = Math.floor(h / 24);
    return `${d}d ${h % 24}h`;
  }
  return `${h}h ${m}m`;
}

export function Expiry({ at }: { at: number }) {
  return <span className="tabular-nums text-muted">{formatExpiry(at)}</span>;
}
```

Create `web/src/components/AccountsTable.tsx`:
```tsx
import {
  createColumnHelper, flexRender, getCoreRowModel, getSortedRowModel,
  useReactTable, type SortingState,
} from "@tanstack/react-table";
import { ArrowsClockwiseIcon, TrashIcon } from "@phosphor-icons/react";
import { useState } from "react";
import type { Account } from "../lib/types";
import { StatusBadge } from "./StatusBadge";
import { Expiry } from "./Expiry";

interface Props {
  accounts: Account[];
  onRefresh: (email: string) => void;
  onDelete: (email: string) => void;
  busyEmail?: string;
}

const col = createColumnHelper<Account>();

export function AccountsTable({ accounts, onRefresh, onDelete, busyEmail }: Props) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const columns = [
    col.accessor("email", { header: "Email", cell: (c) => <span className="font-medium">{c.getValue()}</span> }),
    col.accessor("status", { header: "Status", cell: (c) => <StatusBadge status={c.getValue()} /> }),
    col.accessor("accessExpiresAt", { header: "Access expiry", cell: (c) => <Expiry at={c.getValue()} /> }),
    col.accessor("lastRefreshedAt", { header: "Last refresh", cell: (c) => <Expiry at={c.getValue()} /> }),
    col.display({
      id: "actions", header: "",
      cell: (c) => {
        const email = c.row.original.email;
        const busy = busyEmail === email;
        return (
          <div className="flex justify-end gap-1">
            <button
              type="button" aria-label={`Refresh ${email}`} disabled={busy}
              onClick={() => onRefresh(email)}
              className="rounded-md p-2 text-muted hover:bg-panel hover:text-ink focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-40"
            >
              <ArrowsClockwiseIcon size={18} className={busy ? "animate-spin" : undefined} />
            </button>
            <button
              type="button" aria-label={`Delete ${email}`}
              onClick={() => onDelete(email)}
              className="rounded-md p-2 text-muted hover:bg-panel hover:text-err focus-visible:outline-2 focus-visible:outline-brand"
            >
              <TrashIcon size={18} />
            </button>
          </div>
        );
      },
    }),
  ];

  const table = useReactTable({
    data: accounts, columns,
    state: { sorting }, onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(), getSortedRowModel: getSortedRowModel(),
  });

  return (
    <table className="w-full border-collapse text-sm">
      <thead>
        {table.getHeaderGroups().map((hg) => (
          <tr key={hg.id} className="border-b border-border text-left text-muted">
            {hg.headers.map((h) => (
              <th
                key={h.id} className="cursor-pointer select-none px-4 py-3 font-medium"
                onClick={h.column.getToggleSortingHandler()}
              >
                {flexRender(h.column.columnDef.header, h.getContext())}
                {{ asc: " ↑", desc: " ↓" }[h.column.getIsSorted() as string] ?? ""}
              </th>
            ))}
          </tr>
        ))}
      </thead>
      <tbody>
        {table.getRowModel().rows.map((row) => (
          <tr key={row.id} className="border-b border-border/60 hover:bg-surface">
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id} className="px-4 py-3">
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /e/autoclaw/web && npx vitest run src/components/`
Expected: PASS. (Both `Expiry` and `AccountsTable` suites.)

- [ ] **Step 5: Commit**

```bash
cd /e/autoclaw && git add web/src/components && git commit -m "feat(web): status badge, expiry countdown, accounts table"
```

---

### Task 5: Manual login flow (Add Account)

**Files:**
- Create: `web/src/components/AddAccount.tsx`
- Test: `web/src/components/AddAccount.test.tsx`

**Interfaces:**
- Consumes: `api.ts` (`startManualLogin`), `types.ts`.
- Produces: `AddAccount()` — a button that on click calls `startManualLogin()`, opens `oauthUrl` in a new tab (`window.open`), and shows an inline "waiting for you to finish signing in…" state until an SSE-driven refetch surfaces the new account (the parent re-renders the list). Errors render inline. Exposes an `openUrl` prop (default `window.open`) for testability.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/AddAccount.test.tsx`:
```tsx
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ReactNode } from "react";
import { AddAccount } from "./AddAccount";

afterEach(() => vi.restoreAllMocks());

function wrap(ui: ReactNode) {
  return <QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>;
}

test("clicking Add starts a login and opens the oauth url", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(
    JSON.stringify({ state: "st1", oauth_url: "https://g/o" }),
    { status: 200, headers: { "content-type": "application/json" } },
  )));
  const openUrl = vi.fn();
  render(wrap(<AddAccount openUrl={openUrl} />));
  await userEvent.click(screen.getByRole("button", { name: /add account/i }));
  await waitFor(() => expect(openUrl).toHaveBeenCalledWith("https://g/o"));
  expect(screen.getByText(/waiting/i)).toBeInTheDocument();
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /e/autoclaw/web && npx vitest run src/components/AddAccount.test.tsx`
Expected: FAIL — cannot resolve `./AddAccount`.

- [ ] **Step 3: Write the component**

Create `web/src/components/AddAccount.tsx`:
```tsx
import { PlusIcon } from "@phosphor-icons/react";
import { useState } from "react";
import { startManualLogin } from "../lib/api";

interface Props {
  openUrl?: (url: string) => void;
}

export function AddAccount({ openUrl = (u) => window.open(u, "_blank", "noopener") }: Props) {
  const [phase, setPhase] = useState<"idle" | "waiting">("idle");
  const [error, setError] = useState<string | null>(null);

  async function start() {
    setError(null);
    try {
      const { oauthUrl } = await startManualLogin();
      openUrl(oauthUrl);
      setPhase("waiting");
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to start login");
    }
  }

  return (
    <div className="flex items-center gap-3">
      <button
        type="button" onClick={start} disabled={phase === "waiting"}
        className="inline-flex items-center gap-2 rounded-lg bg-brand px-3 py-2 text-sm font-medium text-brand-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50"
      >
        <PlusIcon size={16} weight="bold" /> Add account
      </button>
      {phase === "waiting" && (
        <span className="text-sm text-muted">Waiting for you to finish signing in…</span>
      )}
      {error && <span className="text-sm text-err">{error}</span>}
    </div>
  );
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /e/autoclaw/web && npx vitest run src/components/AddAccount.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /e/autoclaw && git add web/src/components/AddAccount.tsx web/src/components/AddAccount.test.tsx && git commit -m "feat(web): manual Add-account login flow"
```

---

### Task 6: App shell — wire everything, states, and cockpit theme

**Files:**
- Modify: `web/src/App.tsx`, `web/src/App.test.tsx`
- Create: `web/src/components/StatTiles.tsx`, `web/src/components/EmptyState.tsx`

**Interfaces:**
- Consumes: `queryClient.ts`, `useAccounts`/`useRefreshAccount`/`useRefreshAll`/`useDeleteAccount`, `useServerEvents`, `AccountsTable`, `AddAccount`, `StatusBadge`, `types.ts`.
- Produces: the wired dashboard — `QueryClientProvider` at the root; header with app name + `StatTiles` (total / active / needs-relogin counts) + `AddAccount` + a disabled "Bulk login (Plan 4)" button + "Refresh all"; the table with loading (skeleton), empty (`EmptyState`), and error states; `useServerEvents` live. Everything on the dark cockpit theme.

- [ ] **Step 1: Write the failing test**

Replace `web/src/App.test.tsx`:
```tsx
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { App } from "./App";
import { queryClient } from "./lib/queryClient";

afterEach(() => {
  queryClient.clear();
  vi.restoreAllMocks();
});

function mockAccounts(list: unknown[]) {
  vi.stubGlobal("EventSource", class { onmessage = null; close() {} } as unknown as typeof EventSource);
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (String(url).includes("/api/accounts")) {
      return new Response(JSON.stringify(list), { status: 200, headers: { "content-type": "application/json" } });
    }
    return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
  }));
}

test("empty state teaches how to add an account", async () => {
  mockAccounts([]);
  render(<App />);
  await waitFor(() => expect(screen.getByText(/no accounts yet/i)).toBeInTheDocument());
});

test("renders accounts and a total count when the list is non-empty", async () => {
  mockAccounts([
    { email: "a@x.com", user_id: "u", status: "active", access_expires_at: 9_999_999_999, refresh_expires_at: 9_999_999_999, last_refreshed_at: 1, added_at: 1 },
  ]);
  render(<App />);
  await waitFor(() => expect(screen.getByText("a@x.com")).toBeInTheDocument());
  expect(screen.getByText(/bulk login/i)).toBeDisabled();
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /e/autoclaw/web && npx vitest run src/App.test.tsx`
Expected: FAIL — new symbols/states missing.

- [ ] **Step 3: Write the supporting components**

Create `web/src/components/StatTiles.tsx`:
```tsx
import type { Account } from "../lib/types";

export function StatTiles({ accounts }: { accounts: Account[] }) {
  const total = accounts.length;
  const active = accounts.filter((a) => a.status === "active").length;
  const relogin = accounts.filter((a) => a.status === "needs_relogin").length;
  const tiles = [
    { label: "Total", value: total, color: "var(--color-ink)" },
    { label: "Active", value: active, color: "var(--color-ok)" },
    { label: "Need re-login", value: relogin, color: "var(--color-warn)" },
  ];
  return (
    <div className="flex gap-6">
      {tiles.map((t) => (
        <div key={t.label} className="flex flex-col">
          <span className="text-2xl font-semibold tabular-nums" style={{ color: t.color }}>{t.value}</span>
          <span className="text-xs text-muted">{t.label}</span>
        </div>
      ))}
    </div>
  );
}
```

Create `web/src/components/EmptyState.tsx`:
```tsx
import { UserPlusIcon } from "@phosphor-icons/react";

export function EmptyState() {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed border-border py-16 text-center">
      <UserPlusIcon size={32} className="text-muted" />
      <p className="text-ink">No accounts yet</p>
      <p className="max-w-sm text-sm text-muted">
        Click <span className="text-ink">Add account</span> to sign in with Google. Bulk stealth login arrives in Plan 4.
      </p>
    </div>
  );
}
```

- [ ] **Step 4: Write the wired App**

Replace `web/src/App.tsx`:
```tsx
import { QueryClientProvider } from "@tanstack/react-query";
import { ArrowsClockwiseIcon, UsersThreeIcon } from "@phosphor-icons/react";
import { queryClient } from "./lib/queryClient";
import { useAccounts, useDeleteAccount, useRefreshAccount, useRefreshAll } from "./hooks/useAccounts";
import { useServerEvents } from "./hooks/useServerEvents";
import { AccountsTable } from "./components/AccountsTable";
import { AddAccount } from "./components/AddAccount";
import { StatTiles } from "./components/StatTiles";
import { EmptyState } from "./components/EmptyState";

function Dashboard() {
  useServerEvents();
  const accounts = useAccounts();
  const refreshOne = useRefreshAccount();
  const refreshAll = useRefreshAll();
  const del = useDeleteAccount();
  const list = accounts.data ?? [];

  return (
    <div className="mx-auto max-w-5xl px-6 py-8">
      <header className="mb-8 flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <UsersThreeIcon size={26} weight="duotone" className="text-brand" />
          <h1 className="text-xl font-semibold">GogoClaw</h1>
        </div>
        <StatTiles accounts={list} />
      </header>

      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <AddAccount />
          <button
            type="button" disabled title="Bulk stealth login arrives in Plan 4"
            className="rounded-lg border border-border px-3 py-2 text-sm text-muted disabled:opacity-50"
          >
            Bulk login
          </button>
        </div>
        <button
          type="button" onClick={() => refreshAll.mutate()} disabled={refreshAll.isPending || list.length === 0}
          className="inline-flex items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50"
        >
          <ArrowsClockwiseIcon size={16} className={refreshAll.isPending ? "animate-spin" : undefined} /> Refresh all
        </button>
      </div>

      <div className="overflow-x-auto rounded-xl border border-border bg-surface">
        {accounts.isLoading ? (
          <div className="space-y-3 p-4" aria-label="Loading accounts">
            {[0, 1, 2].map((i) => <div key={i} className="h-10 animate-pulse rounded-md bg-panel" />)}
          </div>
        ) : accounts.isError ? (
          <p className="p-6 text-sm text-err">Failed to load accounts: {(accounts.error as Error).message}</p>
        ) : list.length === 0 ? (
          <EmptyState />
        ) : (
          <AccountsTable
            accounts={list}
            onRefresh={(email) => refreshOne.mutate(email)}
            onDelete={(email) => del.mutate(email)}
            busyEmail={refreshOne.isPending ? (refreshOne.variables as string) : undefined}
          />
        )}
      </div>
    </div>
  );
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <Dashboard />
    </QueryClientProvider>
  );
}
```

- [ ] **Step 5: Run tests, build, and verify the Go embed**

Run:
```bash
cd /e/autoclaw/web && npx vitest run && npm run build
cd /e/autoclaw && go build ./... && go test ./internal/server/ -count=1
```
Expected: all Vitest tests pass; `npm run build` refreshes `web/dist`; Go builds; server tests still pass (placeholder-UI test still finds "GogoClaw" in the built index.html title).

- [ ] **Step 6: Manual smoke (documented)**

Run the backend (`go run ./cmd/gogoclaw`) and open `http://127.0.0.1:18432`. Expected: the dark dashboard renders; with no accounts it shows the empty state; clicking "Add account" opens a Google OAuth tab and, after login, the account appears in the table via SSE. (A real Google login exercises the full stack.)

- [ ] **Step 7: Commit the built dashboard**

```bash
cd /e/autoclaw
git add web/src web/dist
git commit -m "feat(web): wire dashboard shell, stat tiles, empty/loading/error states"
```

---

## Notes for the implementer

- **Build order matters:** always `npm run build` before `go build`/committing so the embedded `web/dist` matches the source. `web/dist` is committed on purpose (Go binary builds without Node).
- **`@tailwindcss/vite` v4:** tokens live in `src/index.css` under `@theme`; utilities like `bg-surface`, `text-muted`, `text-brand`, `outline-brand` come from those custom `--color-*` vars. No `tailwind.config.js`, no PostCSS.
- **TanStack Table is v8 stable:** `useReactTable` + `getCoreRowModel`/`getSortedRowModel` + `flexRender` + `createColumnHelper`. Do NOT use the v9 `@beta` `useTable`/`tableFeatures` API.
- **Phosphor icons** are imported as named `*Icon` components (`@phosphor-icons/react`), with `size`/`weight` props.
- **Bulk login stays disabled** — it's Plan 4. The button renders disabled with a tooltip; do not call `startManualLogin` for it.
- Deferred to Plan 4: the Bulk-login modal (paste `email:password`, per-line progress) and any auto-driver wiring.
