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
          {cfg.data?.eligibleCount ?? 0} eligible
          {cfg.data?.current ? ` · active ${cfg.data.current}` : ""}
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
        Models: <code className="text-ink">glm-5.2</code>, <code className="text-ink">glm-5-turbo</code>,{" "}
        <code className="text-ink">auto</code>.
      </p>
    </section>
  );
}
