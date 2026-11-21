import "server-only";

import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";

import type { Agency } from "@/lib/api/client";
import { loadAgencies } from "@/lib/api/load";
import { sessionCookieName } from "@/lib/auth/cookie";
import { isSignInEnabled } from "@/lib/auth/server";
import { isFlagEnabled } from "@/lib/flags/server";

import { SUBSCRIPTIONS_SIGN_IN_PATH } from "./state";

export async function isSubscriptionsEnabled(): Promise<boolean> {
  return (
    (await isSignInEnabled()) && (await isFlagEnabled("webhook-subscriptions"))
  );
}

export async function isDirectedPromptsEnabled(): Promise<boolean> {
  return isFlagEnabled("webhook-directed-prompts");
}

export async function requireSubscriptions(): Promise<void> {
  if (!(await isSubscriptionsEnabled())) {
    notFound();
  }
  if (!(await cookies()).has(sessionCookieName())) {
    redirect(SUBSCRIPTIONS_SIGN_IN_PATH);
  }
}

export type AgencyChoices =
  | { ok: true; agencies: Agency[]; names: ReadonlyMap<string, string> }
  | { ok: false; names: ReadonlyMap<string, string> };

export async function loadAgencyChoices(): Promise<AgencyChoices> {
  const loaded = await loadAgencies();
  if (!loaded.ok) {
    return { ok: false, names: new Map() };
  }
  return {
    ok: true,
    agencies: loaded.data,
    names: new Map(loaded.data.map((agency) => [agency.slug, agency.name])),
  };
}
