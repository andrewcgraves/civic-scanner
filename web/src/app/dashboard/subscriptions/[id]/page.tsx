import type { Metadata } from "next";
import Link from "next/link";
import { notFound, redirect } from "next/navigation";

import { MeetingTime } from "@/components/ui/date-time";
import { Notice, UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { Section } from "@/components/ui/section";
import { readApiAsUser, type UserApiResult } from "@/lib/auth/api";
import type { components } from "@/lib/api/v1";

import { DeliveryLog } from "../_components/delivery-log";
import { PausedNotice } from "../_components/paused-notice";
import { SendTest } from "../_components/send-test";
import { FailureStreak, StatusBadge } from "../_components/subscription-status";
import { TemplateSummary } from "../_components/template-summary";
import {
  DELIVERIES_PAGE_SIZE,
  deliveriesPath,
  readCursor,
} from "../deliveries";
import {
  isDirectedPromptsEnabled,
  loadAgencyChoices,
  requireSubscriptions,
} from "../server";
import {
  SUBSCRIPTIONS_PATH,
  SUBSCRIPTIONS_SIGN_IN_PATH,
  agencyName,
  editPath,
  formatLabel,
  isSubscriptionId,
  readOutcome,
  subscriptionPath,
  type Subscription,
} from "../state";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Subscription",
  robots: { index: false },
};

type DeliveryList = components["schemas"]["WebhookDeliveryList"];

function breadcrumb(name: string) {
  return [
    { href: "/dashboard", label: "Dashboard" },
    { href: SUBSCRIPTIONS_PATH, label: "Subscriptions" },
    { label: name },
  ];
}

function StatusNotice({
  subscription,
  name,
}: {
  subscription: Subscription;
  name: string;
}) {
  if (subscription.status === "paused") {
    return (
      <div className="mb-12">
        <PausedNotice
          id={subscription.id}
          agencyName={name}
          reason={subscription.pausedReason}
          pausedAt={subscription.pausedAt}
        />
      </div>
    );
  }
  if (subscription.consecutiveFailedEvents <= 0) {
    return null;
  }
  return (
    <div className="mb-12">
      <FailureStreak count={subscription.consecutiveFailedEvents} />
    </div>
  );
}

function LogBody({
  id,
  log,
  name,
}: {
  id: string;
  log: UserApiResult<DeliveryList>;
  name: string;
}) {
  if (log.status === "ok") {
    const { nextCursor } = log.data;
    return (
      <DeliveryLog
        deliveries={log.data.deliveries}
        agencyName={name}
        olderHref={nextCursor === null ? null : deliveriesPath(id, nextCursor)}
      />
    );
  }
  if (log.status === "rejected") {
    return (
      <Notice tone="error" title="That page of the log cannot be shown">
        <Link href={subscriptionPath(id)} className="underline">
          Show the newest deliveries
        </Link>
      </Notice>
    );
  }
  return (
    <Notice tone="error">
      The delivery log is unavailable right now. Please try again later.{" "}
      <Link href={subscriptionPath(id)} className="underline">
        Try again
      </Link>
    </Notice>
  );
}

export default async function SubscriptionPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  await requireSubscriptions();
  const { id } = await params;
  if (!isSubscriptionId(id)) {
    notFound();
  }
  const query = await searchParams;
  const cursor = readCursor(query);
  const [result, log, choices, prompts] = await Promise.all([
    readApiAsUser((api) =>
      api.GET("/webhook-subscriptions/{id}", { params: { path: { id } } }),
    ),
    readApiAsUser((api) =>
      api.GET("/webhook-subscriptions/{id}/deliveries", {
        params: {
          path: { id },
          query: {
            limit: DELIVERIES_PAGE_SIZE,
            ...(cursor === undefined ? {} : { cursor }),
          },
        },
      }),
    ),
    loadAgencyChoices(),
    isDirectedPromptsEnabled(),
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
        <PageHeader
          title="Subscription"
          breadcrumb={breadcrumb("Subscription")}
        />
        <UnavailableNotice what="This subscription is" />
      </>
    );
  }
  const subscription = result.data;
  const name = agencyName(choices.names, subscription.agency);
  const outcome = readOutcome(query);
  const prompt = prompts ? subscription.prompt?.trim() : undefined;
  const directed = prompt !== undefined && prompt !== "";

  return (
    <>
      <PageHeader title={name} breadcrumb={breadcrumb(name)}>
        <StatusBadge subscription={subscription} />
        <span className="font-mono text-sm break-all">{subscription.url}</span>
        <span className="text-sm">
          {formatLabel(subscription.format)} · added{" "}
          <MeetingTime value={subscription.createdAt} format="short-date" /> ·{" "}
          <Link
            href={editPath(id)}
            aria-label={`Edit ${name}`}
            className="link"
          >
            Edit
          </Link>
        </span>
      </PageHeader>
      {outcome?.kind === "resumed" ? (
        <div role="status" className="mb-6">
          <Notice title={`${name} is active again`}>
            Webhooks are sent to it from now on.
          </Notice>
        </div>
      ) : null}
      <StatusNotice subscription={subscription} name={name} />
      {subscription.format === "custom" && subscription.template !== null ? (
        <TemplateSummary
          id={id}
          agencyName={name}
          template={subscription.template}
        />
      ) : null}
      {directed ? (
        <Section
          title="Directed prompt"
          description="Each agenda and transcript is read against this. A webhook is sent only when something matches, with a short message about it. Meetings with nothing relevant are skipped and do not appear in the log below."
          actions={
            <Link
              href={editPath(id)}
              aria-label={`Edit the prompt for ${name}`}
              className="link"
            >
              Edit prompt
            </Link>
          }
        >
          <div className="rounded-lg border border-line bg-surface px-5 py-4">
            <p className="max-w-[70ch] leading-relaxed whitespace-pre-wrap">
              {prompt}
            </p>
          </div>
        </Section>
      ) : null}
      <Section
        title="Send a test"
        description={`Sends one sample event, marked as a test, to this URL now, signed with the current secret. Paused subscriptions can be tested too. Tests are not added to the log.${directed ? " A test does not use the directed prompt." : ""}`}
      >
        <SendTest id={id} />
      </Section>
      <Section
        title="Recent deliveries"
        description="Newest first. A failed attempt is retried for about a day."
        actions={
          <Link href={subscriptionPath(id)} className="link">
            {cursor === undefined ? "Refresh" : "Back to the newest"}
          </Link>
        }
      >
        <LogBody id={id} log={log} name={name} />
      </Section>
    </>
  );
}
