import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { BulkLogin, parseCreds } from "./BulkLogin";

afterEach(() => vi.restoreAllMocks());

test("parseCreds splits on the first colon only, so passwords containing a colon survive", () => {
  // A regression to `line.split(":")` would truncate the password at the first
  // colon (e.g. "p" instead of "p:a$$w:ord"), so this fails under that bug.
  expect(parseCreds("x@y.com:p:a$$w:ord")).toEqual([{ email: "x@y.com", password: "p:a$$w:ord" }]);
});

test("parseCreds skips blank lines", () => {
  expect(parseCreds("a@x.com:pw\n\n  \n")).toHaveLength(1);
});

test("parseCreds skips lines with no colon", () => {
  const result = parseCreds("noColonHere\na@x.com:pw");
  expect(result).toHaveLength(1);
  expect(result[0]).toEqual({ email: "a@x.com", password: "pw" });
});

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

test("preserves the full password when it contains a colon", async () => {
  const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => new Response(
    JSON.stringify({ started: [{ email: "x@y.com", state: "s1" }], errors: [] }),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} />);
  await userEvent.type(
    screen.getByLabelText(/credentials/i),
    "x@y.com:p:a$$w:ord",
  );
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.objectContaining({ method: "POST" })));
  const sent = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(sent.accounts).toEqual([{ email: "x@y.com", password: "p:a$$w:ord" }]);
});
