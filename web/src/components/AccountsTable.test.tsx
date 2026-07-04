import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import type { Account } from "../lib/types";
import { AccountsTable } from "./AccountsTable";

const accts: Account[] = [
  { email: "a@x.com", userId: "u1", status: "active", accessExpiresAt: 9_999_999_999, refreshExpiresAt: 9_999_999_999, lastRefreshedAt: 1, addedAt: 1 },
  { email: "b@x.com", userId: "u2", status: "needs_relogin", accessExpiresAt: 0, refreshExpiresAt: 0, lastRefreshedAt: 1, addedAt: 1 },
];

test("renders a row per account with the email and status", () => {
  render(<AccountsTable accounts={accts} onRefresh={() => {}} onDelete={() => {}} />);
  expect(screen.getByText("a@x.com")).toBeInTheDocument();
  expect(screen.getByText("b@x.com")).toBeInTheDocument();
  expect(screen.getByText(/needs re-?login/i)).toBeInTheDocument();
});

test("clicking Refresh fires onRefresh with the row email", async () => {
  const onRefresh = vi.fn();
  render(<AccountsTable accounts={accts} onRefresh={onRefresh} onDelete={() => {}} />);
  const rows = screen.getAllByRole("row");
  // header row + 2 data rows; click the first data row's Refresh button
  const firstRowRefresh = within(rows[1]).getByRole("button", { name: /refresh/i });
  await userEvent.click(firstRowRefresh);
  expect(onRefresh).toHaveBeenCalledWith("a@x.com");
});
