import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { LoginProxyPanel } from "./LoginProxyPanel";

afterEach(() => vi.restoreAllMocks());

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
}

function renderWithClient(ui: ReactNode) {
  return render(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);
}

test("loads the pool and saves edited proxies as an array", async () => {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (url === "/api/login/proxy-pool" && init?.method === "POST") return json({ status: "ok" });
    if (url === "/api/login/proxy-pool") return json({ proxies: ["http://a:8080"], count: 1 });
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);

  renderWithClient(<LoginProxyPanel />);
  await waitFor(() => expect(screen.getByDisplayValue("http://a:8080")).toBeInTheDocument());

  const box = screen.getByLabelText(/proxy pool/i);
  await userEvent.clear(box);
  await userEvent.type(box, "http://b:8080\nsocks5://c:1080");
  await userEvent.click(screen.getByRole("button", { name: /save pool/i }));

  await waitFor(() => {
    const post = fetchMock.mock.calls.find(
      (c) => c[0] === "/api/login/proxy-pool" && (c[1] as RequestInit)?.method === "POST",
    );
    expect(post).toBeTruthy();
    expect(JSON.parse((post![1] as RequestInit).body as string).proxies).toEqual([
      "http://b:8080",
      "socks5://c:1080",
    ]);
  });
});
