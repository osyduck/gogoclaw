import { CheckCircleIcon, CircleNotchIcon, ClockIcon, XCircleIcon, XIcon } from "@phosphor-icons/react";
import { useEffect, useRef, useState } from "react";
import { bulkLogin, getLoginProxies, loginStatus } from "../lib/api";
import type { Provider } from "../lib/types";

// parseCreds turns "email:password" lines into credential objects, skipping blanks.
export function parseCreds(text: string): { email: string; password: string }[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .filter((line) => line.indexOf(":") >= 0)
    .map((line) => {
      const idx = line.indexOf(":");
      return { email: line.slice(0, idx).trim(), password: line.slice(idx + 1).trim() };
    })
    .filter((c) => c.email && c.password);
}

type RowStatus = "pending" | "ok" | "error" | "timeout";
interface Row {
  email: string;
  state?: string; // absent when the server rejected the account outright
  status: RowStatus;
  error?: string;
  steps?: string[]; // live per-action log streamed from the sidecar
}

type RowUpdate = { state?: string; steps?: string[]; status?: RowStatus; error?: string };

interface Props {
  onClose: () => void;
  pollMs?: number; // how often to re-check each in-flight login
  timeoutMs?: number; // give up on a still-pending login after this long
}

export function BulkLogin({ onClose, pollMs = 2000, timeoutMs = 180_000 }: Props) {
  const [text, setText] = useState("");
  const [provider, setProvider] = useState<Provider>("google");
  const [busy, setBusy] = useState(false);
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [useProxyPool, setUseProxyPool] = useState(false);
  const [poolCount, setPoolCount] = useState(0);
  const startedAt = useRef(0);

  useEffect(() => {
    getLoginProxies()
      .then((p) => setPoolCount(p.count))
      .catch(() => setPoolCount(0));
  }, []);

  async function submit() {
    setError(null);
    const creds = parseCreds(text);
    if (creds.length === 0) {
      setError("Enter at least one email:password line");
      return;
    }
    setBusy(true);
    try {
      const res = await bulkLogin(creds, provider, useProxyPool && poolCount > 0);
      startedAt.current = Date.now();
      setRows([
        ...res.started.map((s): Row => ({ email: s.email, state: s.state, status: "pending" })),
        ...res.errors.map((e): Row => ({ email: e.email, status: "error", error: e.error })),
      ]);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "bulk login failed";
      setError(
        /not configured/i.test(msg)
          ? "Stealth auto-login isn't configured on the server. Install the sidecar (pip install -r sidecar/requirements.txt) and set GOGOCLAW_PYTHON to that interpreter."
          : msg,
      );
    } finally {
      setBusy(false);
    }
  }

  // Poll every still-pending login until it reaches a terminal state (the
  // callback completed → "ok", the sidecar reported a reason → "error") or the
  // timeout elapses. This is what turns "N started" into real per-account
  // visibility: the user can see each account sign in, fail with a reason, or
  // stall — instead of guessing whether anything happened.
  useEffect(() => {
    if (!rows) return;
    const pending = rows.filter((r) => r.status === "pending" && r.state);
    if (pending.length === 0) return;
    const id = setInterval(async () => {
      const updates = await Promise.all(
        pending.map(async (r): Promise<RowUpdate | null> => {
          try {
            const s = await loginStatus(r.state!);
            const base: RowUpdate = { state: r.state, steps: s.steps };
            if (s.status === "ok") return { ...base, status: "ok" };
            if (s.status === "error") return { ...base, status: "error", error: s.error };
            if (Date.now() - startedAt.current > timeoutMs) return { ...base, status: "timeout" };
            return base; // still pending, but its step log advanced
          } catch {
            // transient (server busy / stream hiccup) — keep the row pending.
            if (Date.now() - startedAt.current > timeoutMs) return { state: r.state, status: "timeout" };
            return null;
          }
        }),
      );
      const changes = updates.filter((u): u is RowUpdate => u !== null);
      if (changes.length === 0) return;
      setRows((cur) =>
        cur?.map((r) => {
          const u = changes.find((x) => x.state === r.state);
          if (!u) return r;
          return {
            ...r,
            steps: u.steps ?? r.steps,
            status: u.status ?? r.status,
            error: "error" in u ? u.error : r.error,
          };
        }) ?? cur,
      );
    }, pollMs);
    return () => clearInterval(id);
  }, [rows, pollMs, timeoutMs]);

  const pendingCount = rows?.filter((r) => r.status === "pending").length ?? 0;
  const okCount = rows?.filter((r) => r.status === "ok").length ?? 0;
  const failCount = rows?.filter((r) => r.status === "error" || r.status === "timeout").length ?? 0;

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
          id="bulk-creds" value={text} onChange={(e) => setText(e.target.value)} rows={6}
          className="w-full rounded-lg border border-border bg-panel p-3 font-mono text-sm text-ink outline-none focus-visible:border-brand"
          placeholder={"you@gmail.com:password\n…"}
        />
        <p className="mt-2 text-xs text-muted">Credentials are used once for sign-in and never stored.</p>

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

        {error && <p className="mt-3 text-sm text-err">{error}</p>}

        {rows && (
          <div className="mt-4">
            <div className="mb-2 flex items-center gap-3 text-xs text-muted">
              {pendingCount > 0 && <span className="inline-flex items-center gap-1"><CircleNotchIcon size={13} className="animate-spin" /> {pendingCount} signing in</span>}
              {okCount > 0 && <span className="text-ok">{okCount} signed in</span>}
              {failCount > 0 && <span className="text-err">{failCount} failed</span>}
            </div>
            <ul className="max-h-80 space-y-2 overflow-y-auto rounded-lg border border-border bg-panel p-2 text-sm">
              {rows.map((r) => (
                <li key={r.email} className="px-1 py-1">
                  <div className="flex items-center justify-between gap-3">
                    <span className="min-w-0 flex-1 truncate font-mono text-xs" title={r.email}>{r.email}</span>
                    <StatusChip row={r} />
                  </div>
                  {r.steps && r.steps.length > 0 && <StepLog steps={r.steps} status={r.status} />}
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className="mt-5 flex justify-end gap-2">
          <button type="button" onClick={onClose} className="rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink">
            Close
          </button>
          <button
            type="button" onClick={submit} disabled={busy || pendingCount > 0}
            className="rounded-lg bg-brand px-3 py-2 text-sm font-medium text-brand-ink hover:brightness-110 disabled:opacity-50"
          >
            {busy ? "Starting…" : pendingCount > 0 ? "Signing in…" : "Start bulk login"}
          </button>
        </div>
      </div>
    </div>
  );
}

// StepLog renders the sidecar's per-action progress as a live terminal — one
// line per step, ERROR lines in red, the final success line in green — and
// keeps itself scrolled to the newest line.
function StepLog({ steps, status }: { steps: string[]; status: RowStatus }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [steps.length]);
  return (
    <div
      ref={ref}
      aria-label="login steps"
      className="mt-1 max-h-28 overflow-y-auto rounded bg-black/50 p-2 font-mono text-[11px] leading-relaxed text-muted"
    >
      {steps.map((s, i) => {
        const isError = s.includes("ERROR");
        const isDone = status === "ok" && i === steps.length - 1;
        return (
          <div key={i} className={isError ? "text-err" : isDone ? "text-ok" : undefined}>
            {s}
          </div>
        );
      })}
    </div>
  );
}

function StatusChip({ row }: { row: Row }) {
  switch (row.status) {
    case "ok":
      return <span className="inline-flex items-center gap-1 text-xs text-ok"><CheckCircleIcon size={15} weight="fill" /> Signed in</span>;
    case "error":
      return (
        <span className="inline-flex items-center gap-1 text-xs text-err" title={row.error}>
          <XCircleIcon size={15} weight="fill" /> {shortReason(row.error)}
        </span>
      );
    case "timeout":
      return <span className="inline-flex items-center gap-1 text-xs text-warn"><ClockIcon size={15} /> Timed out</span>;
    default:
      return <span className="inline-flex items-center gap-1 text-xs text-muted"><CircleNotchIcon size={15} className="animate-spin" /> Signing in…</span>;
  }
}

// shortReason trims the server's error to something that fits the row, keeping
// the meaningful tail (e.g. "stealth login failed: password rejected").
function shortReason(reason?: string): string {
  if (!reason) return "Failed";
  const cleaned = reason.replace(/^driver:\s*/, "").replace(/^stealth login failed:\s*/, "");
  return cleaned.length > 42 ? `${cleaned.slice(0, 41)}…` : cleaned;
}
