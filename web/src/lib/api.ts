import type { Account, LoginSession, LoginStart } from "./types";

async function req(path: string, init?: RequestInit): Promise<unknown> {
  const res = await fetch(path, init);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    const msg = (body as { error?: string }).error ?? `request failed (${res.status})`;
    throw new Error(msg);
  }
  return body;
}

interface RawAccount {
  email: string; user_id: string; status: Account["status"];
  access_expires_at: number; refresh_expires_at: number;
  last_refreshed_at: number; added_at: number;
}

export async function listAccounts(): Promise<Account[]> {
  const raw = (await req("/api/accounts")) as RawAccount[];
  return raw.map((a) => ({
    email: a.email, userId: a.user_id, status: a.status,
    accessExpiresAt: a.access_expires_at, refreshExpiresAt: a.refresh_expires_at,
    lastRefreshedAt: a.last_refreshed_at, addedAt: a.added_at,
  }));
}

export async function startManualLogin(): Promise<LoginStart> {
  const r = (await req("/api/login/start", {
    method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({ mode: "manual" }),
  })) as { state: string; oauth_url: string };
  return { state: r.state, oauthUrl: r.oauth_url };
}

export async function loginStatus(state: string): Promise<LoginSession> {
  return (await req(`/api/login/status?state=${encodeURIComponent(state)}`)) as LoginSession;
}

export async function refreshAccount(email: string): Promise<void> {
  await req(`/api/accounts/${encodeURIComponent(email)}/refresh`, { method: "POST" });
}

export async function refreshAll(): Promise<void> {
  await req("/api/accounts/refresh-all", { method: "POST" });
}

export async function deleteAccount(email: string): Promise<void> {
  await req(`/api/accounts/${encodeURIComponent(email)}`, { method: "DELETE" });
}
