import { parseSessionState, type SessionState } from "./session-state";

export async function loadSessionState(): Promise<SessionState | null> {
  try {
    const response = await fetch("/auth/session", {
      cache: "no-store",
      headers: { accept: "application/json" },
    });
    return response.ok ? parseSessionState(await response.json()) : null;
  } catch {
    return null;
  }
}
