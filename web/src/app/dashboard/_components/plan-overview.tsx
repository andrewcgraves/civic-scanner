"use client";

import { useEffect, useState } from "react";

import { EmptyState } from "@/components/ui/empty-state";
import { Notice } from "@/components/ui/notice";
import { Section } from "@/components/ui/section";
import { loadSessionState } from "@/lib/auth/load-session";
import type { SessionState, UserPlan } from "@/lib/auth/session-state";

import { DASHBOARD_SIGN_IN_PATH, SUBSCRIPTIONS_PATH } from "../paths";

import { PlanCard, PlanCardLoading } from "./plan-card";

export type PlanStatus =
  | { kind: "loading" }
  | { kind: "known"; plan: UserPlan }
  | { kind: "signed-out" }
  | { kind: "unavailable" };

export function planStatus(state: SessionState | null): PlanStatus {
  if (state === null) {
    return { kind: "unavailable" };
  }
  if (!state.signedIn) {
    return { kind: "signed-out" };
  }
  return state.plan === null
    ? { kind: "unavailable" }
    : { kind: "known", plan: state.plan };
}

function PlanBody({
  status,
  billing,
  subscriptions,
}: {
  status: PlanStatus;
  billing: boolean;
  subscriptions: boolean;
}) {
  switch (status.kind) {
    case "loading":
      return <PlanCardLoading />;
    case "known":
      return (
        <PlanCard
          plan={status.plan}
          billing={billing}
          subscriptions={subscriptions}
        />
      );
    case "signed-out":
      return (
        <Notice title="You are signed out">
          <a href={DASHBOARD_SIGN_IN_PATH} className="link">
            Sign in again
          </a>{" "}
          to see your plan.
        </Notice>
      );
    case "unavailable":
      return (
        <Notice tone="error" title="Your plan cannot be loaded right now">
          Please try again later. Nothing about your plan or billing has
          changed.
        </Notice>
      );
  }
}

function SubscriptionsSummary({ paid }: { paid: boolean }) {
  return (
    <div className="rounded-lg border border-line bg-surface px-5 py-5 sm:px-6">
      <p className="max-w-[62ch] text-ink-muted">
        {paid
          ? "Choose agencies and get a webhook call when they post a meeting or agenda."
          : "Webhook subscriptions are part of the paid plan."}
      </p>
      <p className="mt-3">
        <a href={SUBSCRIPTIONS_PATH} className="link">
          {paid ? "Manage your subscriptions" : "See what subscriptions do"}
        </a>
      </p>
    </div>
  );
}

export function PlanOverview({
  billing,
  subscriptions = false,
}: {
  billing: boolean;
  subscriptions?: boolean;
}) {
  const [status, setStatus] = useState<PlanStatus>({ kind: "loading" });

  useEffect(() => {
    let current = true;
    void loadSessionState().then((loaded) => {
      if (current) {
        setStatus(planStatus(loaded));
      }
    });
    return () => {
      current = false;
    };
  }, []);

  const paid = status.kind === "known" && status.plan !== "free";

  return (
    <div>
      <Section title="Your plan">
        <PlanBody
          status={status}
          billing={billing}
          subscriptions={subscriptions}
        />
      </Section>
      <Section title="Subscriptions">
        {subscriptions ? (
          <SubscriptionsSummary paid={paid} />
        ) : (
          <EmptyState title="Webhook subscriptions are coming soon">
            Choose agencies and get a webhook call when they post a meeting or
            agenda.{" "}
            {paid
              ? "They will be set up here."
              : "Subscriptions will be part of the paid plan."}
          </EmptyState>
        )}
      </Section>
    </div>
  );
}
