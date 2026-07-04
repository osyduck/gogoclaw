import { PlusIcon } from "@phosphor-icons/react";
import { useState } from "react";
import { startManualLogin } from "../lib/api";
import type { Provider } from "../lib/types";

interface Props {
  openUrl?: (url: string) => void;
}

export function AddAccount({ openUrl = (u) => window.open(u, "_blank", "noopener") }: Props) {
  const [phase, setPhase] = useState<"idle" | "waiting">("idle");
  const [starting, setStarting] = useState(false);
  const [provider, setProvider] = useState<Provider>("google");
  const [error, setError] = useState<string | null>(null);

  async function start() {
    setError(null);
    setPhase("idle");
    setStarting(true);
    try {
      const { oauthUrl } = await startManualLogin(provider);
      openUrl(oauthUrl);
      setPhase("waiting");
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to start login");
    } finally {
      setStarting(false);
    }
  }

  return (
    <div className="flex items-center gap-3">
      <button
        type="button" onClick={start} disabled={starting}
        className="inline-flex items-center gap-2 rounded-lg bg-brand px-3 py-2 text-sm font-medium text-brand-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50"
      >
        <PlusIcon size={16} weight="bold" /> Add account
      </button>
      <select
        aria-label="Login provider" value={provider}
        onChange={(e) => setProvider(e.target.value as Provider)}
        className="rounded-lg border border-border bg-panel px-2 py-2 text-sm text-ink"
      >
        <option value="google">Direct Google</option>
        <option value="zai">via chat.z.ai</option>
      </select>
      {phase === "waiting" && (
        <span className="text-sm text-muted">Waiting for you to finish signing in…</span>
      )}
      {error && <span className="text-sm text-err">{error}</span>}
    </div>
  );
}
