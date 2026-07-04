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

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
}

test("posts creds, then polls each login to a terminal per-account status", async () => {
  const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
    if (url === "/api/login/bulk") {
      return json({ started: [{ email: "a@x.com", state: "s1" }, { email: "b@x.com", state: "s2" }], errors: [] });
    }
    // Status polling: a@x.com signs in, b@x.com is rejected with a reason.
    if (url.includes("state=s1")) return json({ state: "s1", status: "ok", email: "a@x.com" });
    if (url.includes("state=s2")) return json({ state: "s2", status: "error", error: "stealth login failed: password rejected" });
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  await userEvent.type(screen.getByLabelText(/credentials/i), "a@x.com:pw1\nb@x.com:pw2");
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.objectContaining({ method: "POST" })));
  const sent = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(sent.accounts).toEqual([
    { email: "a@x.com", password: "pw1" },
    { email: "b@x.com", password: "pw2" },
  ]);

  // Both rows render immediately, then resolve to their real outcomes.
  expect(screen.getByText("a@x.com")).toBeInTheDocument();
  await waitFor(() => expect(screen.getByText("Signed in")).toBeInTheDocument());
  await waitFor(() => expect(screen.getByText(/password rejected/i)).toBeInTheDocument());
  expect(screen.getByText(/1 signed in/i)).toBeInTheDocument();
  expect(screen.getByText(/1 failed/i)).toBeInTheDocument();
});

test("shows a server-rejected account as failed without polling it", async () => {
  const fetchMock = vi.fn(async (url: string) => {
    if (url === "/api/login/bulk") {
      return json({ started: [], errors: [{ email: "bad@x.com", error: "generate identity: boom" }] });
    }
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  await userEvent.type(screen.getByLabelText(/credentials/i), "bad@x.com:pw");
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(screen.getByText("bad@x.com")).toBeInTheDocument());
  expect(screen.getByText(/1 failed/i)).toBeInTheDocument();
  // Only the bulk POST is called; a row that never started is not polled.
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

test("surfaces a helpful hint when auto-login is not configured", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(
    JSON.stringify({ error: "auto login not configured" }),
    { status: 501, headers: { "content-type": "application/json" } },
  )));

  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  await userEvent.type(screen.getByLabelText(/credentials/i), "a@x.com:pw");
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(screen.getByText(/GOGOCLAW_PYTHON/)).toBeInTheDocument());
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
