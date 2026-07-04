export type AccountStatus = "active" | "refresh_failed" | "needs_relogin";

export interface Account {
  email: string;
  userId: string;
  status: AccountStatus;
  accessExpiresAt: number;
  refreshExpiresAt: number;
  lastRefreshedAt: number;
  addedAt: number;
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
}

export interface GogoEvent {
  type: string;
  email?: string;
  detail?: string;
}
