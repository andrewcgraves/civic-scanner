"use server";

import { notFound, redirect } from "next/navigation";

import { callApiAsUser, type UserApiResult } from "@/lib/auth/api";
import type { components } from "@/lib/api/v1";

import {
  BILLING_SIGN_IN_PATH,
  billingErrorPath,
  isBillingEnabled,
  stripeRedirectUrl,
  type BillingError,
} from "./server";

type BillingRedirect = components["schemas"]["BillingRedirect"];

function destination(
  result: UserApiResult<BillingRedirect>,
  conflict: BillingError,
): string {
  switch (result.status) {
    case "ok":
      return (
        stripeRedirectUrl(result.data.url) ?? billingErrorPath("unavailable")
      );
    case "signed-out":
      return BILLING_SIGN_IN_PATH;
    case "conflict":
      return billingErrorPath(conflict);
    default:
      return billingErrorPath("unavailable");
  }
}

export async function startCheckoutAction(): Promise<void> {
  if (!(await isBillingEnabled())) {
    notFound();
  }
  const result = await callApiAsUser((api) =>
    api.POST("/billing/checkout-session"),
  );
  redirect(destination(result, "already-subscribed"));
}

export async function openBillingPortalAction(): Promise<void> {
  if (!(await isBillingEnabled())) {
    notFound();
  }
  const result = await callApiAsUser((api) =>
    api.POST("/billing/portal-session"),
  );
  redirect(destination(result, "no-subscription"));
}
