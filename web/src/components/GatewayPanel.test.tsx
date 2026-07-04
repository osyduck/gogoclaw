import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { GatewayPanel } from "./GatewayPanel";

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

describe("GatewayPanel", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("loads config and shows the current mode", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          mode: "round_robin",
          n: 5,
          api_key_set: false,
          eligible_count: 2,
          current: "a@x.com",
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );
    render(wrap(<GatewayPanel />));
    await waitFor(() => expect(screen.getByLabelText(/rotation mode/i)).toHaveValue("round_robin"));
    expect(screen.getByText(/2 eligible/i)).toBeInTheDocument();
  });

  it("saves a new mode", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ mode: "sticky", n: 5, api_key_set: false, eligible_count: 1, current: "" }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      )
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "ok" }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    render(wrap(<GatewayPanel />));
    await waitFor(() => expect(screen.getByLabelText(/rotation mode/i)).toHaveValue("sticky"));
    await userEvent.selectOptions(screen.getByLabelText(/rotation mode/i), "round_robin");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const post = fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === "POST");
      expect(post).toBeDefined();
      const body = JSON.parse((post![1] as RequestInit).body as string);
      expect(body.mode).toBe("round_robin");
    });
  });
});
