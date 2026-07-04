import { afterEach, expect, test, vi } from "vitest";
import { listAccounts, startManualLogin, refreshAccount, deleteAccount, bulkLogin } from "./api";

afterEach(() => vi.restoreAllMocks());

function mockFetch(status: number, body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), {
    status, headers: { "content-type": "application/json" },
  })));
}

test("listAccounts maps snake_case fields to the Account shape", async () => {
  mockFetch(200, [{
    email: "a@x.com", user_id: "u1", status: "active",
    access_expires_at: 111, refresh_expires_at: 222, last_refreshed_at: 333, added_at: 444,
    balance: 2300,
  }]);
  const accts = await listAccounts();
  expect(accts).toHaveLength(1);
  expect(accts[0]).toEqual({
    email: "a@x.com", userId: "u1", status: "active",
    accessExpiresAt: 111, refreshExpiresAt: 222, lastRefreshedAt: 333, addedAt: 444,
    balance: 2300,
  });
});

test("startManualLogin returns state + oauthUrl", async () => {
  mockFetch(200, { state: "st1", oauth_url: "https://g/o" });
  const r = await startManualLogin();
  expect(r).toEqual({ state: "st1", oauthUrl: "https://g/o" });
  expect(fetch).toHaveBeenCalledWith("/api/login/start", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ mode: "manual", provider: "google" }),
  });
});

test("refreshAccount throws on a 404", async () => {
  mockFetch(404, { error: "account not found" });
  await expect(refreshAccount("missing@x.com")).rejects.toThrow(/account not found/);
});

test("refreshAccount URL-encodes the email path param", async () => {
  mockFetch(200, { status: "ok" });
  await refreshAccount("a+b@x.com");
  expect(fetch).toHaveBeenCalledWith("/api/accounts/a%2Bb%40x.com/refresh", { method: "POST" });
});

test("deleteAccount URL-encodes the email path param", async () => {
  mockFetch(200, { status: "ok" });
  await deleteAccount("a+b@x.com");
  expect(fetch).toHaveBeenCalledWith("/api/accounts/a%2Bb%40x.com", { method: "DELETE" });
});

test("bulkLogin sends the selected provider", async () => {
  const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => new Response(
    JSON.stringify({ started: [], errors: [] }),
    { status: 200, headers: { "content-type": "application/json" } },
  ));
  vi.stubGlobal("fetch", fetchMock);
  await bulkLogin([{ email: "a@x.com", password: "pw" }], "zai");
  const body = JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  expect(body.provider).toBe("zai");
});
