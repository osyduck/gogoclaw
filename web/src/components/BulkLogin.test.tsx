import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { BulkLogin } from "./BulkLogin";

afterEach(() => vi.restoreAllMocks());

test("parses email:password lines and posts them, showing results", async () => {
  const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => new Response(
    JSON.stringify({ started: [{ email: "a@x.com", state: "s1" }, { email: "b@x.com", state: "s2" }], errors: [] }),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} />);
  await userEvent.type(
    screen.getByLabelText(/credentials/i),
    "a@x.com:pw1\nb@x.com:pw2",
  );
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.objectContaining({ method: "POST" })));
  const sent = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(sent.accounts).toEqual([
    { email: "a@x.com", password: "pw1" },
    { email: "b@x.com", password: "pw2" },
  ]);
  await waitFor(() => expect(screen.getByText(/2 started/i)).toBeInTheDocument());
});
