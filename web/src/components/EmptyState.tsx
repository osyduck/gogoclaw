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
