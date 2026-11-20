import type { Metadata } from "next";
import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { Notice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { Section } from "@/components/ui/section";
import { sessionCookieName } from "@/lib/auth/cookie";
import {
  openBillingPortalAction,
  startCheckoutAction,
} from "@/lib/billing/actions";
import {
  BILLING_ERRORS,
  BILLING_SIGN_IN_PATH,
  readBillingError,
  requireBilling,
} from "@/lib/billing/server";

import { ActionButton } from "./_components/action-button";

export const dynamic = "force-dynamic";

export const metadata: Metadata = { title: "Billing" };

export default async function BillingPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  await requireBilling();
  if (!(await cookies()).has(sessionCookieName())) {
    redirect(BILLING_SIGN_IN_PATH);
  }
  const error = readBillingError((await searchParams).error);

  return (
    <>
      <PageHeader
        title="Billing"
        description="Start the paid plan, or manage it in Stripe's billing portal."
      />
      {error === null ? null : (
        <div className="mb-10">
          <Notice tone="error">{BILLING_ERRORS[error]}</Notice>
        </div>
      )}
      <Section
        title="Paid plan"
        description="Payment is taken on a secure Stripe page. You come back here when you are done."
      >
        <ActionButton action={startCheckoutAction}>
          Start the paid plan
        </ActionButton>
      </Section>
      <Section
        title="Manage billing"
        description="Stripe's billing portal is where you manage your subscription."
      >
        <ActionButton action={openBillingPortalAction}>
          Open the billing portal
        </ActionButton>
      </Section>
    </>
  );
}
