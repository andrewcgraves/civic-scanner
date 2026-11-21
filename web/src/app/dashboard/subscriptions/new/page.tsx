import type { Metadata } from "next";

import { UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";

import { SubscriptionForm } from "../_components/subscription-form";
import {
  isDirectedPromptsEnabled,
  loadAgencyChoices,
  requireSubscriptions,
} from "../server";
import { SUBSCRIPTIONS_PATH } from "../state";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Add a subscription",
  robots: { index: false },
};

export default async function NewSubscriptionPage() {
  await requireSubscriptions();
  const [choices, prompts] = await Promise.all([
    loadAgencyChoices(),
    isDirectedPromptsEnabled(),
  ]);

  return (
    <>
      <PageHeader
        title="Add a subscription"
        breadcrumb={[
          { href: "/dashboard", label: "Dashboard" },
          { href: SUBSCRIPTIONS_PATH, label: "Subscriptions" },
          { label: "Add a subscription" },
        ]}
      />
      {choices.ok ? (
        <SubscriptionForm
          prompts={prompts}
          mode={{
            kind: "create",
            agencies: choices.agencies.map(({ slug, name }) => ({
              slug,
              name,
            })),
          }}
        />
      ) : (
        <UnavailableNotice what="The list of agencies is" />
      )}
    </>
  );
}
