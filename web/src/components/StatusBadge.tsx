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
