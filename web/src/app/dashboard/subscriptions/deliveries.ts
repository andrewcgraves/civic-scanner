import type { components } from "@/lib/api/v1";

import { subscriptionPath } from "./state";

export type Delivery = components["schemas"]["WebhookDelivery"];
export type DeliveryAttempt = components["schemas"]["WebhookDeliveryAttempt"];
export type TestResult = components["schemas"]["WebhookTestResult"];
export type EventType = components["schemas"]["WebhookEventType"];
export type PausedReason =
  components["schemas"]["WebhookSubscriptionPausedReason"];

export const PAUSE_AFTER_FAILED_EVENTS = 5;
export const DELIVERIES_PAGE_SIZE = 20;

const CURSOR_LIMIT = 1024;
const PROBLEM_LIMIT = 300;
const DEFAULT_RETRY_AFTER_SECONDS = 60;
const MAX_RETRY_AFTER_SECONDS = 3600;

export const EVENT_TYPES: readonly EventType[] = [
  "agenda_published",
  "agenda_changed",
  "transcript_ready",
];

export const DEFAULT_EVENT_TYPE: EventType = "agenda_published";

export const EVENT_LABELS: Record<EventType, string> = {
  agenda_published: "Agenda published",
  agenda_changed: "Agenda changed",
  transcript_ready: "Transcript ready",
};

export function isEventType(value: unknown): value is EventType {
  return EVENT_TYPES.includes(value as EventType);
}

export function eventLabel(value: string): string {
  return isEventType(value) ? EVENT_LABELS[value] : value;
}

export type TestState =
  | { kind: "idle" }
  | { kind: "sent"; result: TestResult }
  | { kind: "limited"; retryAfter: number }
  | { kind: "gone" }
  | { kind: "upgrade" }
  | { kind: "unavailable" };

export const IDLE_TEST: TestState = { kind: "idle" };

export function readRetryAfter(value: string | null): number {
  const trimmed = value?.trim() ?? "";
  if (!/^\d+$/.test(trimmed)) {
    return DEFAULT_RETRY_AFTER_SECONDS;
  }
  return Math.min(Math.max(Number(trimmed), 1), MAX_RETRY_AFTER_SECONDS);
}

export function readCursor(
  params: Record<string, string | string[] | undefined>,
): string | undefined {
  const { cursor } = params;
  if (
    typeof cursor !== "string" ||
    cursor === "" ||
    cursor.length > CURSOR_LIMIT
  ) {
    return undefined;
  }
  return cursor;
}

export function deliveriesPath(id: string, cursor: string): string {
  return `${subscriptionPath(id)}?cursor=${encodeURIComponent(cursor)}`;
}

export function formatDuration(ms: number): string {
  if (ms < 1000) {
    return `${String(Math.round(ms))} ms`;
  }
  return `${(ms / 1000).toFixed(1)} s`;
}

export function problem(error: string | null): string | undefined {
  const trimmed = error?.trim().slice(0, PROBLEM_LIMIT) ?? "";
  return trimmed === "" ? undefined : trimmed.replace(/[.!?]+$/, "");
}

export function attemptResult(attempt: {
  statusCode: number | null;
  error: string | null;
}): string {
  if (attempt.statusCode !== null) {
    return `Answered ${String(attempt.statusCode)}`;
  }
  const why = problem(attempt.error);
  return why === undefined ? "No answer" : `No answer: ${why}`;
}

export function attemptCount(count: number): string {
  return count === 1 ? "1 attempt" : `${String(count)} attempts`;
}

export function failureStreak(count: number): string {
  return count === 1
    ? "The last event failed to deliver"
    : `The last ${String(count)} events failed to deliver`;
}
