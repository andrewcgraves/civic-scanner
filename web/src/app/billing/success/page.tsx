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
        description="Thank you. Stripe has received your payment."
      />
      <div className="space-y-6">
        <Notice title="Your plan status is not shown yet">
          Your plan will show on this site once Stripe&apos;s confirmation has
          been processed.
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
