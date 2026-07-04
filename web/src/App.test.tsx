import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { App } from "./App";
import { queryClient } from "./lib/queryClient";

afterEach(() => {
  queryClient.clear();
  vi.restoreAllMocks();
});

function mockAccounts(list: unknown[]) {
  vi.stubGlobal("EventSource", class { onmessage = null; close() {} } as unknown as typeof EventSource);
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (String(url).includes("/api/accounts")) {
      return new Response(JSON.stringify(list), { status: 200, headers: { "content-type": "application/json" } });
    }
    return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
  }));
}

test("empty state teaches how to add an account", async () => {
  mockAccounts([]);
  render(<App />);
  await waitFor(() => expect(screen.getByText(/no accounts yet/i)).toBeInTheDocument());
});

test("renders accounts and a total count when the list is non-empty", async () => {
  mockAccounts([
    { email: "a@x.com", user_id: "u", status: "active", access_expires_at: 9_999_999_999, refresh_expires_at: 9_999_999_999, last_refreshed_at: 1, added_at: 1 },
  ]);
  render(<App />);
  await waitFor(() => expect(screen.getByText("a@x.com")).toBeInTheDocument());
  expect(screen.getByRole("button", { name: /bulk login/i })).toBeEnabled();
});
