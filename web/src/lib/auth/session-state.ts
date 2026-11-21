import type { components } from "@/lib/api/v1";

export type UserRole = components["schemas"]["User"]["role"];

export type UserPlan = components["schemas"]["User"]["plan"];

export type SessionState =
  | { signedIn: false }
  | {
      signedIn: true;
      email: string | null;
      role: UserRole | null;
      plan: UserPlan | null;
    };

const ROLES: readonly UserRole[] = ["user", "admin"];

const PLANS: readonly UserPlan[] = ["free", "paid"];

function parseRole(value: unknown): UserRole | null {
  return ROLES.find((role) => role === value) ?? null;
}

function parsePlan(value: unknown): UserPlan | null {
  return PLANS.find((plan) => plan === value) ?? null;
}

export function parseSessionState(value: unknown): SessionState | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const { signedIn, email, role, plan } = value as Record<string, unknown>;
  if (signedIn === false) {
    return { signedIn: false };
  }
  if (signedIn === true) {
    return {
      signedIn: true,
      email: typeof email === "string" ? email : null,
      role: parseRole(role),
      plan: parsePlan(plan),
    };
  }
  return null;
}
