import type { components } from "@/lib/api/v1";

import { SUBSCRIPTIONS_PATH } from "../paths";

export { SUBSCRIPTIONS_PATH };

export type Subscription = components["schemas"]["WebhookSubscription"];
export type SubscriptionFormat = components["schemas"]["WebhookFormat"];

export const NEW_SUBSCRIPTION_PATH = `${SUBSCRIPTIONS_PATH}/new`;
export const SUBSCRIPTIONS_SIGN_IN_PATH = `/auth/sign-in?returnTo=${encodeURIComponent(SUBSCRIPTIONS_PATH)}`;

export const MAX_SUBSCRIPTIONS = 25;
export const URL_MAX_LENGTH = 2048;

const ECHO_LIMIT = 4096;
const MESSAGE_LIMIT = 300;
const SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

const FORM_CODES = new Set([
  "invalid_body",
  "invalid_payload_version",
  "invalid_status",
  "nothing_to_update",
]);

export const FORM_ERROR = "Check the form and try again.";

export function subscriptionPath(id: string): string {
  return `${SUBSCRIPTIONS_PATH}/${encodeURIComponent(id)}`;
}

export function editPath(id: string): string {
  return `${subscriptionPath(id)}/edit`;
}

export const RETURN_TO_SUBSCRIPTION = "subscription";

export type SubscriptionField = "agency" | "url" | "format";

export type SubscriptionValues = Record<SubscriptionField, string>;

export const FORMATS: readonly SubscriptionFormat[] = [
  "standard",
  "discord",
  "slack",
  "custom",
];

export const DEFAULT_FORMAT: SubscriptionFormat = "standard";

export const FORMAT_LABELS: Record<SubscriptionFormat, string> = {
  standard: "Standard JSON",
  discord: "Discord",
  slack: "Slack",
  custom: "Custom JSON",
};

export function isFormat(value: unknown): value is SubscriptionFormat {
  return FORMATS.includes(value as SubscriptionFormat);
}

export function formatChoices(current: string): SubscriptionFormat[] {
  return FORMATS.filter((format) => format !== "custom" || current === format);
}

export function formatLabel(value: string): string {
  return isFormat(value) ? FORMAT_LABELS[value] : value;
}

export type FieldErrors = Partial<Record<SubscriptionField, string>>;

export const FIELD_LABELS: Record<SubscriptionField, string> = {
  agency: "Agency",
  url: "Endpoint URL",
  format: "Message format",
};

export type FormState =
  | { kind: "idle"; values: SubscriptionValues }
  | {
      kind: "invalid";
      values: SubscriptionValues;
      errors: FieldErrors;
      message?: string;
    }
  | { kind: "duplicate"; values: SubscriptionValues }
  | { kind: "limit"; values: SubscriptionValues }
  | { kind: "upgrade"; values: SubscriptionValues }
  | { kind: "gone"; values: SubscriptionValues }
  | { kind: "unavailable"; values: SubscriptionValues }
  | {
      kind: "created";
      agency: string;
      url: string;
      secret: string;
    };

export type ActionState =
  | { kind: "idle" }
  | { kind: "rotated"; secret: string }
  | { kind: "gone" }
  | { kind: "upgrade" }
  | { kind: "unavailable" };

export const IDLE_ACTION: ActionState = { kind: "idle" };

export interface Outcome {
  kind: "updated" | "paused" | "resumed" | "deleted";
  agency: string;
}

const OUTCOME_KINDS = ["updated", "paused", "resumed", "deleted"] as const;

export function initialForm(values: Partial<SubscriptionValues>): FormState {
  return {
    kind: "idle",
    values: {
      agency: values.agency ?? "",
      url: values.url ?? "",
      format: values.format ?? DEFAULT_FORMAT,
    },
  };
}

function text(value: FormDataEntryValue | null): string {
  return typeof value === "string" ? value.slice(0, ECHO_LIMIT) : "";
}

export function readValues(formData: FormData): SubscriptionValues {
  return {
    agency: text(formData.get("agency")).trim(),
    url: text(formData.get("url")),
    format: text(formData.get("format")).trim() || DEFAULT_FORMAT,
  };
}

export function readId(formData: FormData): string {
  return text(formData.get("id")).trim();
}

export function readReturnsToSubscription(formData: FormData): boolean {
  return formData.get("returnTo") === RETURN_TO_SUBSCRIPTION;
}

export function isSubscriptionId(value: string): boolean {
  return UUID_PATTERN.test(value);
}

export function isSlug(value: string): boolean {
  return value.length <= 100 && SLUG_PATTERN.test(value);
}

export function checkUrl(url: string): string | undefined {
  const trimmed = url.trim();
  if (trimmed === "") {
    return "Enter the URL to send webhooks to.";
  }
  if (new TextEncoder().encode(trimmed).length > URL_MAX_LENGTH) {
    return "The URL must be at most 2,048 bytes.";
  }
  return undefined;
}

export function checkFormat(format: string): string | undefined {
  return isFormat(format) ? undefined : "Choose a message format.";
}

export function checkValues(values: SubscriptionValues): FieldErrors {
  const errors: FieldErrors = {};
  if (!isSlug(values.agency)) {
    errors.agency = "Choose an agency.";
  }
  const url = checkUrl(values.url);
  if (url !== undefined) {
    errors.url = url;
  }
  const format = checkFormat(values.format);
  if (format !== undefined) {
    errors.format = format;
  }
  return errors;
}

function record(body: unknown): Record<string, unknown> {
  return typeof body === "object" && body !== null
    ? (body as Record<string, unknown>)
    : {};
}

export function sentence(value: unknown): string | undefined {
  if (typeof value !== "string" || value.trim() === "") {
    return undefined;
  }
  const trimmed = value.trim().slice(0, MESSAGE_LIMIT);
  const capital = `${trimmed.charAt(0).toUpperCase()}${trimmed.slice(1)}`;
  return /[.!?]$/.test(capital) ? capital : `${capital}.`;
}

function isField(value: unknown): value is SubscriptionField {
  return value === "agency" || value === "url" || value === "format";
}

function listedErrors(fields: unknown): FieldErrors {
  const errors: FieldErrors = {};
  if (!Array.isArray(fields)) {
    return errors;
  }
  for (const entry of fields as unknown[]) {
    const { field, message } = record(entry);
    const text = sentence(message);
    if (isField(field) && text !== undefined) {
      errors[field] ??= text;
    }
  }
  return errors;
}

export function rejection(body: unknown): {
  errors: FieldErrors;
  message?: string;
} {
  const { code, message, fields } = record(body);
  const listed = listedErrors(fields);
  if (Object.keys(listed).length > 0) {
    return { errors: listed };
  }
  if (code === "unknown_agency") {
    return { errors: { agency: "Choose an agency from the list." } };
  }
  if (code === "invalid_format") {
    return { errors: { format: "Choose a message format from the list." } };
  }
  if (typeof code !== "string" || FORM_CODES.has(code)) {
    return { errors: {}, message: FORM_ERROR };
  }
  return { errors: { url: sentence(message) ?? FORM_ERROR } };
}

function single(value: string | string[] | undefined): string | undefined {
  return typeof value === "string" ? value : undefined;
}

export function outcomePath(kind: Outcome["kind"], agency: string): string {
  return `${SUBSCRIPTIONS_PATH}?${kind}=${encodeURIComponent(agency)}`;
}

export function subscriptionOutcomePath(
  id: string,
  kind: Outcome["kind"],
  agency: string,
): string {
  return `${subscriptionPath(id)}?${kind}=${encodeURIComponent(agency)}`;
}

export function readOutcome(
  params: Record<string, string | string[] | undefined>,
): Outcome | null {
  for (const kind of OUTCOME_KINDS) {
    const agency = single(params[kind]);
    if (agency !== undefined && isSlug(agency)) {
      return { kind, agency };
    }
  }
  return null;
}

export function agencyName(
  names: ReadonlyMap<string, string>,
  slug: string,
): string {
  return names.get(slug) ?? slug;
}
