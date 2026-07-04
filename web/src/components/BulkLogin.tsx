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
