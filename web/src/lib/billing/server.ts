import "server-only";

import { notFound } from "next/navigation";

import { isSignInEnabled } from "@/lib/auth/server";
import { isFlagEnabled } from "@/lib/flags/server";

export const BILLING_PATH = "/billing";

export const BILLING_SIGN_IN_PATH = `/auth/sign-in?returnTo=${encodeURIComponent(BILLING_PATH)}`;

export const BILLING_ERRORS = {
  "already-subscribed":
    "You already have a subscription. Use the billing portal to change or cancel it.",
  "no-subscription":
    "There is no billing account yet. Start the paid plan first.",
  unavailable: "Billing is unavailable right now. Please try again later.",
} as const;

export type BillingError = keyof typeof BILLING_ERRORS;

export function billingErrorPath(error: BillingError): string {
  return `${BILLING_PATH}?error=${error}`;
}

export function readBillingError(value: unknown): BillingError | null {
  return typeof value === "string" && Object.hasOwn(BILLING_ERRORS, value)
    ? (value as BillingError)
    : null;
}

export async function isBillingEnabled(): Promise<boolean> {
  return (await isSignInEnabled()) && (await isFlagEnabled("billing"));
}

export async function requireBilling(): Promise<void> {
  if (!(await isBillingEnabled())) {
    notFound();
  }
}

export function stripeRedirectUrl(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" || url.username !== "" || url.password !== "") {
    return null;
  }
  return url.href;
}
