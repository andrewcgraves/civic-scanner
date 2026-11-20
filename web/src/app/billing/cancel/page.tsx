import type { Metadata } from "next";
import Link from "next/link";

import { PageHeader } from "@/components/ui/page-header";
import { BILLING_PATH, requireBilling } from "@/lib/billing/server";

export const dynamic = "force-dynamic";

export const metadata: Metadata = { title: "Checkout cancelled" };

export default async function BillingCancelPage() {
  await requireBilling();
  return (
    <>
      <PageHeader
        title="Checkout cancelled"
        description="You have not been charged and nothing has changed."
      />
      <p>
        <Link href={BILLING_PATH} className="link">
          Back to billing
        </Link>
      </p>
    </>
  );
}
