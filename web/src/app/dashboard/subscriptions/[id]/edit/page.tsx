import type { Metadata } from "next";
import { notFound, redirect } from "next/navigation";

import { UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { readApiAsUser } from "@/lib/auth/api";

import { SubscriptionForm } from "../../_components/subscription-form";
import { loadAgencyChoices, requireSubscriptions } from "../../server";
import {
  SUBSCRIPTIONS_PATH,
  SUBSCRIPTIONS_SIGN_IN_PATH,
  agencyName,
  isSubscriptionId,
} from "../../state";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Edit a subscription",
  robots: { index: false },
};

const BREADCRUMB = [
  { href: "/dashboard", label: "Dashboard" },
  { href: SUBSCRIPTIONS_PATH, label: "Subscriptions" },
  { label: "Edit" },
];

export default async function EditSubscriptionPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  await requireSubscriptions();
  const { id } = await params;
  if (!isSubscriptionId(id)) {
    notFound();
  }
  const [result, choices] = await Promise.all([
    readApiAsUser((api) =>
      api.GET("/webhook-subscriptions/{id}", { params: { path: { id } } }),
    ),
    loadAgencyChoices(),
  ]);
  if (result.status === "signed-out") {
    redirect(SUBSCRIPTIONS_SIGN_IN_PATH);
  }
  if (result.status === "not-found") {
    notFound();
  }
  if (result.status === "payment-required") {
    redirect(SUBSCRIPTIONS_PATH);
  }
  if (result.status !== "ok") {
    return (
      <>
        <PageHeader title="Edit a subscription" breadcrumb={BREADCRUMB} />
        <UnavailableNotice what="This subscription is" />
      </>
    );
  }
  const name = agencyName(choices.names, result.data.agency);

  return (
    <>
      <PageHeader title={`Edit ${name}`} breadcrumb={BREADCRUMB} />
      <SubscriptionForm
        mode={{
          kind: "edit",
          id: result.data.id,
          agency: result.data.agency,
          agencyName: name,
          url: result.data.url,
          format: result.data.format,
        }}
      />
    </>
  );
}
