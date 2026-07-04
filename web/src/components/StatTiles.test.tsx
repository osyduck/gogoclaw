import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import type { Account } from "../lib/types";
import { StatTiles } from "./StatTiles";

function acct(over: Partial<Account>): Account {
  return {
    email: "x@x.com", userId: "u", status: "active",
    accessExpiresAt: 0, refreshExpiresAt: 0, lastRefreshedAt: 0, addedAt: 0,
    balance: 0, ...over,
  };
}

test("Credit tile sums balances across accounts, formatted", () => {
  const accts = [
    acct({ email: "a@x.com", balance: 2300 }),
    acct({ email: "b@x.com", balance: 700 }),
    acct({ email: "c@x.com", status: "needs_relogin", balance: 0 }),
  ];
  render(<StatTiles accounts={accts} />);
  const tile = screen.getByText("Credit").previousSibling;
  expect(tile).toHaveTextContent("3,000");
});

test("Credit tile is 0 with no accounts", () => {
  render(<StatTiles accounts={[]} />);
  const tile = screen.getByText("Credit").previousSibling;
  expect(tile).toHaveTextContent("0");
});
