import type { Account } from "../lib/types";

export function StatTiles({ accounts }: { accounts: Account[] }) {
  const total = accounts.length;
  const active = accounts.filter((a) => a.status === "active").length;
  const relogin = accounts.filter((a) => a.status === "needs_relogin").length;
  const failed = accounts.filter((a) => a.status === "refresh_failed").length;
  const credit = accounts.reduce((sum, a) => sum + a.balance, 0);
  const num = (n: number) => n.toLocaleString("en-US");
  const tiles = [
    { label: "Total", value: num(total), color: "var(--color-ink)" },
    { label: "Active", value: num(active), color: "var(--color-ok)" },
    { label: "Need re-login", value: num(relogin), color: "var(--color-warn)" },
    { label: "Failed", value: num(failed), color: "var(--color-err)" },
    { label: "Credit", value: num(credit), color: "var(--color-brand)" },
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
