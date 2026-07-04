import {
  createColumnHelper, flexRender, getCoreRowModel, getSortedRowModel,
  useReactTable, type SortingState,
} from "@tanstack/react-table";
import { ArrowsClockwiseIcon, TrashIcon } from "@phosphor-icons/react";
import { useState } from "react";
import type { Account } from "../lib/types";
import { StatusBadge } from "./StatusBadge";
import { Ago, Expiry } from "./Expiry";

interface Props {
  accounts: Account[];
  onRefresh: (email: string) => void;
  onDelete: (email: string) => void;
  busyEmail?: string;
}

const col = createColumnHelper<Account>();

export function AccountsTable({ accounts, onRefresh, onDelete, busyEmail }: Props) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const columns = [
    col.accessor("email", { header: "Email", cell: (c) => <span className="font-medium">{c.getValue()}</span> }),
    col.accessor("status", { header: "Status", cell: (c) => <StatusBadge status={c.getValue()} /> }),
    col.accessor("balance", {
      header: "Credit",
      cell: (c) => <span className="tabular-nums">{c.getValue().toLocaleString("en-US")}</span>,
    }),
    col.accessor("accessExpiresAt", { header: "Access expiry", cell: (c) => <Expiry at={c.getValue()} /> }),
    col.accessor("lastRefreshedAt", { header: "Last refresh", cell: (c) => <Ago at={c.getValue()} /> }),
    col.display({
      id: "actions", header: "",
      cell: (c) => {
        const email = c.row.original.email;
        const busy = busyEmail === email;
        return (
          <div className="flex justify-end gap-1">
            <button
              type="button" aria-label={`Refresh ${email}`} disabled={busy}
              onClick={() => onRefresh(email)}
              className="rounded-md p-2 text-muted hover:bg-panel hover:text-ink focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-40"
            >
              <ArrowsClockwiseIcon size={18} className={busy ? "animate-spin" : undefined} />
            </button>
            <button
              type="button" aria-label={`Delete ${email}`}
              onClick={() => onDelete(email)}
              className="rounded-md p-2 text-muted hover:bg-panel hover:text-err focus-visible:outline-2 focus-visible:outline-brand"
            >
              <TrashIcon size={18} />
            </button>
          </div>
        );
      },
    }),
  ];

  const table = useReactTable({
    data: accounts, columns,
    state: { sorting }, onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(), getSortedRowModel: getSortedRowModel(),
  });

  return (
    <table className="w-full border-collapse text-sm">
      <thead>
        {table.getHeaderGroups().map((hg) => (
          <tr key={hg.id} className="border-b border-border text-left text-muted">
            {hg.headers.map((h) => (
              <th
                key={h.id}
                className={`select-none px-4 py-3 font-medium ${h.column.getCanSort() ? "cursor-pointer" : ""}`}
                onClick={h.column.getCanSort() ? h.column.getToggleSortingHandler() : undefined}
              >
                {flexRender(h.column.columnDef.header, h.getContext())}
                {{ asc: " ↑", desc: " ↓" }[h.column.getIsSorted() as string] ?? ""}
              </th>
            ))}
          </tr>
        ))}
      </thead>
      <tbody>
        {table.getRowModel().rows.map((row) => (
          <tr key={row.id} className="border-b border-border/60 hover:bg-surface">
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id} className="px-4 py-3">
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
