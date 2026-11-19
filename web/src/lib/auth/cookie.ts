const DEFAULT_SESSION_COOKIE = "wos-session";

export const SESSION_STATE_HEADER = "x-civic-session-state";

export type ProxySessionState = "refreshed" | "unverified";

export function sessionCookieName(): string {
  const configured = process.env.WORKOS_COOKIE_NAME?.trim() ?? "";
  return configured === "" ? DEFAULT_SESSION_COOKIE : configured;
}
