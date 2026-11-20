import type { Metadata } from "next";
import Link from "next/link";

import { Notice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { BILLING_PATH, requireBilling } from "@/lib/billing/server";

export const dynamic = "force-dynamic";

export const metadata: Metadata = { title: "Confirming your payment" };

export default async function BillingSuccessPage() {
  await requireBilling();
  return (
    <>
      <PageHeader
        title="Confirming your payment"
        description="Thank you. Stripe is confirming your payment."
      />
      <div className="space-y-6">
        <Notice title="Your plan is not active yet">
          It turns on once Stripe confirms the payment, which usually takes a
          few moments.
        </Notice>
        <p>
          <Link href={BILLING_PATH} className="link">
            Back to billing
          </Link>
        </p>
      </div>
    </>
  );
}
