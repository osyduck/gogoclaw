export type AccountStatus = "active" | "refresh_failed" | "needs_relogin";

export type Provider = "google" | "zai";

export interface Account {
  email: string;
  userId: string;
  status: AccountStatus;
  accessExpiresAt: number;
  refreshExpiresAt: number;
  lastRefreshedAt: number;
  addedAt: number;
  balance: number;
}

export interface LoginStart {
  state: string;
  oauthUrl: string;
}

export interface LoginSession {
  state: string;
  status: "pending" | "ok" | "error";
  email?: string;
  error?: string;
  steps?: string[];
}

export interface GogoEvent {
  type: string;
  email?: string;
  detail?: string;
}

export type RotationMode = "sticky" | "round_robin" | "rotate_after_n";

export interface ProxyConfigView {
  mode: RotationMode;
  n: number;
  apiKeySet: boolean;
  eligibleCount: number;
  current: string;
}

export interface ProxyConfigUpdate {
  mode: RotationMode;
  n: number;
  // undefined = keep existing key; "" = clear it; non-empty = set it.
  apiKey?: string;
}

export interface LoginProxyPool {
  proxies: string[];
  count: number;
}
