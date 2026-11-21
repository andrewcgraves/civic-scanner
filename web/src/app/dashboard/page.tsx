import type { Metadata } from "next";
import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";

import { PageHeader } from "@/components/ui/page-header";
import { sessionCookieName } from "@/lib/auth/cookie";
import { isSignInEnabled } from "@/lib/auth/server";
import { isBillingEnabled } from "@/lib/billing/server";
import { isFlagEnabled } from "@/lib/flags/server";

import { DashboardNav } from "./_components/dashboard-nav";
import { PlanOverview } from "./_components/plan-overview";
import { DASHBOARD_SIGN_IN_PATH } from "./paths";

export const dynamic = "force-dynamic";

export const metadata: Metadata = { title: "Dashboard" };

export default async function DashboardPage() {
  if (!(await isSignInEnabled())) {
    notFound();
  }
  if (!(await cookies()).has(sessionCookieName())) {
    redirect(DASHBOARD_SIGN_IN_PATH);
  }
  const [billing, subscriptions] = await Promise.all([
    isBillingEnabled(),
    isFlagEnabled("webhook-subscriptions"),
  ]);

  return (
    <>
      <PageHeader
        title="Dashboard"
        description="Your plan, and what comes with it."
      />
      <DashboardNav billing={billing} subscriptions={subscriptions} />
      <PlanOverview billing={billing} subscriptions={subscriptions} />
    </>
  );
}
