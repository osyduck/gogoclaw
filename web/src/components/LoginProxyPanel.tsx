import { useEffect, useState } from "react";
import { useLoginProxies, useSaveLoginProxies } from "../hooks/useLoginProxies";

export function LoginProxyPanel() {
  const pool = useLoginProxies();
  const save = useSaveLoginProxies();
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (pool.data) setText(pool.data.proxies.join("\n"));
  }, [pool.data]);

  if (pool.isLoading) {
    return <div className="h-24 animate-pulse rounded-xl border border-border bg-panel" />;
  }

  const lines = text.split("\n").map((l) => l.trim()).filter(Boolean);

  return (
    <section className="rounded-xl border border-border bg-surface p-4">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold">Add-account proxy pool</h2>
        <span className="text-xs text-muted">{pool.data?.count ?? 0} proxies</span>
      </div>
      <p className="mb-2 text-xs text-muted">
        One proxy URL per line (<code className="text-ink">http://user:pass@host:port</code>, also{" "}
        <code className="text-ink">https://</code> / <code className="text-ink">socks5://</code>). Used only when
        adding accounts with the pool toggle on — routes AutoGLM API calls around a flagged IP (error 630014).
      </p>
      <textarea
        aria-label="Proxy pool"
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={5}
        placeholder={"http://user:pass@1.2.3.4:8080\nsocks5://5.6.7.8:1080"}
        className="w-full rounded-lg border border-border bg-panel p-3 font-mono text-xs text-ink outline-none focus-visible:border-brand"
      />
      {error && <p className="mt-2 text-sm text-err">{error}</p>}
      <div className="mt-3">
        <button
          type="button"
          onClick={() => {
            setError(null);
            save.mutate(lines, {
              onError: (e) => setError(e instanceof Error ? e.message : "save failed"),
            });
          }}
          disabled={save.isPending}
          className="rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50"
        >
          {save.isPending ? "Saving…" : "Save pool"}
        </button>
      </div>
    </section>
  );
}
