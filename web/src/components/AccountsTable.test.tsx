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

test("clicking Delete fires onDelete with the row email", async () => {
  const onDelete = vi.fn();
  render(<AccountsTable accounts={accts} onRefresh={() => {}} onDelete={onDelete} />);
  const rows = screen.getAllByRole("row");
  // header row + 2 data rows; click the first data row's Delete button
  const firstRowDelete = within(rows[1]).getByRole("button", { name: /delete/i });
  await userEvent.click(firstRowDelete);
  expect(onDelete).toHaveBeenCalledWith("a@x.com");
});

test("busyEmail disables only the matching row's Refresh button", () => {
  render(<AccountsTable accounts={accts} onRefresh={() => {}} onDelete={() => {}} busyEmail="a@x.com" />);
  const rows = screen.getAllByRole("row");
  const firstRowRefresh = within(rows[1]).getByRole("button", { name: /refresh/i });
  const secondRowRefresh = within(rows[2]).getByRole("button", { name: /refresh/i });
  expect(firstRowRefresh).toBeDisabled();
  expect(secondRowRefresh).not.toBeDisabled();
});

test("Last refresh column shows an 'ago' value, not 'expired'", () => {
  const now = Math.floor(Date.now() / 1000);
  const recent: Account[] = [
    { email: "c@x.com", userId: "u3", status: "active", accessExpiresAt: 9_999_999_999, refreshExpiresAt: 9_999_999_999, lastRefreshedAt: now - 300, addedAt: 1 },
  ];
  render(<AccountsTable accounts={recent} onRefresh={() => {}} onDelete={() => {}} />);
  const rows = screen.getAllByRole("row");
  const cells = within(rows[1]).getAllByRole("cell");
  const lastRefreshCell = cells[3];
  expect(lastRefreshCell.textContent).toMatch(/ago|just now/);
  expect(lastRefreshCell.textContent).not.toMatch(/expired/);
});
