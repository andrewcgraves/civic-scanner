export type SessionState =
  { signedIn: false } | { signedIn: true; email: string | null };

export function parseSessionState(value: unknown): SessionState | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const { signedIn, email } = value as Record<string, unknown>;
  if (signedIn === false) {
    return { signedIn: false };
  }
  if (signedIn === true) {
    return { signedIn: true, email: typeof email === "string" ? email : null };
  }
  return null;
}
