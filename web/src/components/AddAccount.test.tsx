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

test("the button stays enabled after a login completes, so a second account can be started", async () => {
  const fetchMock = vi.fn(async () => new Response(
    JSON.stringify({ state: "st1", oauth_url: "https://g/o" }),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);
  const openUrl = vi.fn();
  render(wrap(<AddAccount openUrl={openUrl} />));
  const button = screen.getByRole("button", { name: /add account/i });

  await userEvent.click(button);
  await waitFor(() => expect(openUrl).toHaveBeenCalledTimes(1));
  expect(screen.getByText(/waiting/i)).toBeInTheDocument();
  expect(button).not.toBeDisabled();

  await userEvent.click(button);
  await waitFor(() => expect(openUrl).toHaveBeenCalledTimes(2));
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
