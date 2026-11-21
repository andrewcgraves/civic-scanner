import type { Metadata } from "next";
import { notFound, redirect } from "next/navigation";

import { Notice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { Section } from "@/components/ui/section";
import { readApiAsUser } from "@/lib/auth/api";
import { isBillingEnabled } from "@/lib/billing/server";

import { DashboardNav } from "../_components/dashboard-nav";

import { AddButton, SubscriptionList } from "./_components/subscription-list";
import { UpgradePrompt } from "./_components/upgrade-prompt";
import {
  isDirectedPromptsEnabled,
  loadAgencyChoices,
  requireSubscriptions,
} from "./server";
import {
  SUBSCRIPTIONS_PATH,
  SUBSCRIPTIONS_SIGN_IN_PATH,
  agencyName,
  readOutcome,
  type Outcome,
  type Subscription,
} from "./state";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Subscriptions",
  robots: { index: false },
};

const OUTCOMES: Record<Outcome["kind"], (name: string) => [string, string]> = {
  updated: (name) => [
    `${name} is saved`,
    "Webhooks go to the new URL from now on, signed with the same secret.",
  ],
  paused: (name) => [
    `${name} is paused`,
    "No webhooks are sent to it until you resume it.",
  ],
  resumed: (name) => [
    `${name} is active again`,
    "Webhooks are sent to it from now on.",
  ],
  deleted: (name) => [
    `${name} is deleted`,
    "Its webhooks have stopped and its secret is gone.",
  ],
};

function OutcomeNotice({
  outcome,
  names,
}: {
  outcome: Outcome;
  names: ReadonlyMap<string, string>;
}) {
  const [title, body] = OUTCOMES[outcome.kind](
    agencyName(names, outcome.agency),
  );
  return (
    <div role="status" className="mb-6">
      <Notice title={title}>{body}</Notice>
    </div>
  );
}

function ListBody({
  upgrade,
  billing,
  subscriptions,
  names,
  prompts,
}: {
  upgrade: boolean;
  billing: boolean;
  subscriptions: readonly Subscription[] | null;
  names: ReadonlyMap<string, string>;
  prompts: boolean;
}) {
  if (upgrade) {
    return <UpgradePrompt billing={billing} />;
  }
  if (subscriptions === null) {
    return (
      <Notice
        tone="error"
        title="Your subscriptions cannot be loaded right now"
      >
        Please try again later. Nothing about your subscriptions has changed.{" "}
        <a href={SUBSCRIPTIONS_PATH} className="underline">
          Try again
        </a>
      </Notice>
    );
  }
  return (
    <>
      <p className="mb-4 max-w-[65ch] text-ink-muted">
        Each subscription sends a signed request to your URL when the agency
        posts a meeting or an agenda.
      </p>
      <SubscriptionList
        subscriptions={subscriptions}
        names={names}
        prompts={prompts}
      />
    </>
  );
}

export default async function SubscriptionsPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  await requireSubscriptions();
  const [result, choices, billing, prompts] = await Promise.all([
    readApiAsUser((api) => api.GET("/webhook-subscriptions")),
    loadAgencyChoices(),
    isBillingEnabled(),
    isDirectedPromptsEnabled(),
  ]);
  if (result.status === "signed-out") {
    redirect(SUBSCRIPTIONS_SIGN_IN_PATH);
  }
  if (result.status === "not-found") {
    notFound();
  }
  const outcome = readOutcome(await searchParams);
  const subscriptions =
    result.status === "ok" ? result.data.subscriptions : null;

  return (
    <>
      <PageHeader
        title="Dashboard"
        description="Your plan, and what comes with it."
      />
      <DashboardNav billing={billing} subscriptions current="subscriptions" />
      {outcome === null || subscriptions === null ? null : (
        <OutcomeNotice outcome={outcome} names={choices.names} />
      )}
      <Section
        title="Webhook subscriptions"
        actions={
          subscriptions === null ? undefined : (
            <AddButton count={subscriptions.length} />
          )
        }
      >
        <ListBody
          upgrade={result.status === "payment-required"}
          billing={billing}
          subscriptions={subscriptions}
          names={choices.names}
          prompts={prompts}
        />
      </Section>
    </>
  );
}
