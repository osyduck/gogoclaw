import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { useServerEvents } from "./useServerEvents";

class FakeEventSource {
  static last: FakeEventSource | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  closed = false;
  constructor(public url: string) { FakeEventSource.last = this; }
  emit(data: unknown) { this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent); }
  close() { this.closed = true; }
}

afterEach(() => vi.restoreAllMocks());

function Harness() {
  useServerEvents();
  return null;
}

test("an SSE message invalidates the accounts query", async () => {
  vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);
  const qc = new QueryClient();
  const spy = vi.spyOn(qc, "invalidateQueries");
  const { unmount } = render(
    <QueryClientProvider client={qc}><Harness /></QueryClientProvider>,
  );
  FakeEventSource.last!.emit({ type: "refresh:ok", email: "a@x.com" });
  await waitFor(() => expect(spy).toHaveBeenCalledWith({ queryKey: ["accounts"] }));
  unmount();
  expect(FakeEventSource.last!.closed).toBe(true);
});
