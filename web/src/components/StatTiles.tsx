import type { Account } from "../lib/types";

export function StatTiles({ accounts }: { accounts: Account[] }) {
  const total = accounts.length;
  const active = accounts.filter((a) => a.status === "active").length;
  const relogin = accounts.filter((a) => a.status === "needs_relogin").length;
  const failed = accounts.filter((a) => a.status === "refresh_failed").length;
  const tiles = [
    { label: "Total", value: total, color: "var(--color-ink)" },
    { label: "Active", value: active, color: "var(--color-ok)" },
    { label: "Need re-login", value: relogin, color: "var(--color-warn)" },
    { label: "Failed", value: failed, color: "var(--color-err)" },
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
