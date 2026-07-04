import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ReactNode } from "react";
import { AddAccount } from "./AddAccount";

afterEach(() => vi.restoreAllMocks());

function wrap(ui: ReactNode) {
  return <QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>;
}

test("clicking Add starts a login and opens the oauth url", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(
    JSON.stringify({ state: "st1", oauth_url: "https://g/o" }),
    { status: 200, headers: { "content-type": "application/json" } },
  )));
  const openUrl = vi.fn();
  render(wrap(<AddAccount openUrl={openUrl} />));
  await userEvent.click(screen.getByRole("button", { name: /add account/i }));
  await waitFor(() => expect(openUrl).toHaveBeenCalledWith("https://g/o"));
  expect(screen.getByText(/waiting/i)).toBeInTheDocument();
});
