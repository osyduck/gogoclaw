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
