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
  const sentCall = fetchMock.mock.calls.find((c) => c[0] === "/api/login/bulk")!;
  const sent = JSON.parse((sentCall[1] as RequestInit).body as string);
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

test("renders the sidecar's per-action steps as a live terminal", async () => {
  const fetchMock = vi.fn(async (url: string) => {
    if (url === "/api/login/bulk") return json({ started: [{ email: "a@x.com", state: "s1" }], errors: [] });
    // status poll returns a step log plus the terminal outcome
    return json({
      state: "s1", status: "error", error: "stealth login failed: Timeout 30000ms exceeded",
      steps: ["[  0.0s] launch stealth browser", "[  1.2s] enter email", "[ 33.0s] ERROR: Timeout 30000ms exceeded"],
    });
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  await userEvent.type(screen.getByLabelText(/credentials/i), "a@x.com:pw");
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(screen.getByText(/enter email/i)).toBeInTheDocument());
  expect(screen.getByText(/launch stealth browser/i)).toBeInTheDocument();
  expect(screen.getByText(/ERROR: Timeout/i)).toBeInTheDocument();
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
  // A row that never started is not polled (no status?state= fetch happened).
  expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("state="))).toBe(false);
});

test("defaults to Direct Google and can switch to via chat.z.ai", async () => {
  const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => new Response(
    JSON.stringify(url === "/api/login/bulk" ? { started: [], errors: [] } : {}),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);
  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  await userEvent.type(screen.getByLabelText(/credentials/i), "a@x.com:pw");
  await userEvent.click(screen.getByRole("radio", { name: /chat\.z\.ai/i }));
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.anything()));
  const bulkCall = fetchMock.mock.calls.find((c) => c[0] === "/api/login/bulk")!;
  const body = JSON.parse((bulkCall[1] as RequestInit).body as string);
  expect(body.provider).toBe("zai");
});

test("proxy-pool toggle is enabled and sends use_proxy_pool when a pool exists", async () => {
  const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
    if (url === "/api/login/proxy-pool") return json({ proxies: ["http://p:8080"], count: 1 });
    if (url === "/api/login/bulk") return json({ started: [], errors: [] });
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  const cb = await screen.findByRole("checkbox", { name: /proxy pool/i });
  expect(cb).toBeEnabled();
  await userEvent.click(cb);
  await userEvent.type(screen.getByLabelText(/credentials/i), "a@x.com:pw");
  await userEvent.click(screen.getByRole("button", { name: /start bulk login/i }));

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/login/bulk", expect.anything()));
  const call = fetchMock.mock.calls.find((c) => c[0] === "/api/login/bulk")!;
  expect(JSON.parse((call[1] as RequestInit).body as string).use_proxy_pool).toBe(true);
});

test("proxy-pool toggle is disabled when no proxies are configured", async () => {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (url === "/api/login/proxy-pool") return json({ proxies: [], count: 0 });
    return json({});
  }));
  render(<BulkLogin onClose={() => {}} pollMs={10} />);
  const cb = await screen.findByRole("checkbox", { name: /proxy pool/i });
  expect(cb).toBeDisabled();
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
  const sentCall = fetchMock.mock.calls.find((c) => c[0] === "/api/login/bulk")!;
  const sent = JSON.parse((sentCall[1] as RequestInit).body as string);
  expect(sent.accounts).toEqual([{ email: "x@y.com", password: "p:a$$w:ord" }]);
});
