import { QueryClientProvider } from "@tanstack/react-query";
import { ArrowsClockwiseIcon, UsersThreeIcon } from "@phosphor-icons/react";
import { queryClient } from "./lib/queryClient";
import { useAccounts, useDeleteAccount, useRefreshAccount, useRefreshAll } from "./hooks/useAccounts";
import { useServerEvents } from "./hooks/useServerEvents";
import { AccountsTable } from "./components/AccountsTable";
import { AddAccount } from "./components/AddAccount";
import { StatTiles } from "./components/StatTiles";
import { EmptyState } from "./components/EmptyState";

function Dashboard() {
  useServerEvents();
  const accounts = useAccounts();
  const refreshOne = useRefreshAccount();
  const refreshAll = useRefreshAll();
  const del = useDeleteAccount();
  const list = accounts.data ?? [];

  return (
    <div className="mx-auto max-w-5xl px-6 py-8">
      <header className="mb-8 flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <UsersThreeIcon size={26} weight="duotone" className="text-brand" />
          <h1 className="text-xl font-semibold">GogoClaw</h1>
        </div>
        <StatTiles accounts={list} />
      </header>

      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <AddAccount />
          <button
            type="button" disabled title="Bulk stealth login arrives in Plan 4"
            className="rounded-lg border border-border px-3 py-2 text-sm text-muted disabled:opacity-50"
          >
            Bulk login
          </button>
        </div>
        <button
          type="button" onClick={() => refreshAll.mutate()} disabled={refreshAll.isPending || list.length === 0}
          className="inline-flex items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm text-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50"
        >
          <ArrowsClockwiseIcon size={16} className={refreshAll.isPending ? "animate-spin" : undefined} /> Refresh all
        </button>
      </div>

      <div className="overflow-x-auto rounded-xl border border-border bg-surface">
        {accounts.isLoading ? (
          <div className="space-y-3 p-4" aria-label="Loading accounts">
            {[0, 1, 2].map((i) => <div key={i} className="h-10 animate-pulse rounded-md bg-panel" />)}
          </div>
        ) : accounts.isError ? (
          <p className="p-6 text-sm text-err">Failed to load accounts: {(accounts.error as Error).message}</p>
        ) : list.length === 0 ? (
          <EmptyState />
        ) : (
          <AccountsTable
            accounts={list}
            onRefresh={(email) => refreshOne.mutate(email)}
            onDelete={(email) => { if (window.confirm(`Delete ${email}?`)) del.mutate(email); }}
            busyEmail={refreshOne.isPending ? (refreshOne.variables as string) : undefined}
          />
        )}
      </div>
    </div>
  );
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <Dashboard />
    </QueryClientProvider>
  );
}
