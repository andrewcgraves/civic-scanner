const DEFAULT_SESSION_COOKIE = "wos-session";

export function sessionCookieName(): string {
  const configured = process.env.WORKOS_COOKIE_NAME?.trim() ?? "";
  return configured === "" ? DEFAULT_SESSION_COOKIE : configured;
}
