"use client";

import Link from "next/link";
import { useActionState } from "react";

import { Notice } from "@/components/ui/notice";

import { createSubscription, updateSubscription } from "../actions";
import {
  FIELD_LABELS,
  FORMAT_LABELS,
  SUBSCRIPTIONS_PATH,
  URL_MAX_LENGTH,
  formatChoices,
  initialForm,
  type FieldErrors,
  type FormState,
  type SubscriptionField,
} from "../state";

import { PromptField } from "./prompt-field";
import { SecretPanel } from "./secret-panel";
import { BUTTON, INPUT } from "./styles";

export interface AgencyOption {
  slug: string;
  name: string;
}

export type FormMode =
  | { kind: "create"; agencies: readonly AgencyOption[] }
  | {
      kind: "edit";
      id: string;
      agency: string;
      agencyName: string;
      url: string;
      format: string;
      prompt?: string;
    };

const URL_HINT = "An https address on a public host, up to 2,048 characters.";
const FORMAT_HINT =
  "Standard sends signed JSON for your own code. Discord and Slack send a ready-made message to a channel's incoming webhook URL.";

function Problem({ state, mode }: { state: FormState; mode: FormMode }) {
  switch (state.kind) {
    case "invalid":
      return (
        <Notice tone="error" title="Please correct the form">
          {state.message ?? (
            <ul className="list-disc pl-5">
              {(Object.keys(state.errors) as SubscriptionField[]).map(
                (field) => (
                  <li key={field}>
                    <a href={`#${field}`} className="underline">
                      {FIELD_LABELS[field]}
                    </a>
                    : {state.errors[field]}
                  </li>
                ),
              )}
            </ul>
          )}
        </Notice>
      );
    case "duplicate":
      return (
        <Notice tone="error" title="You already follow this agency at this URL">
          Use another URL, or edit the existing subscription.
        </Notice>
      );
    case "limit":
      return (
        <Notice tone="error" title="You have reached the limit">
          You can have at most 25 subscriptions. Delete one to add another.
        </Notice>
      );
    case "upgrade":
      return (
        <Notice tone="error" title="Subscriptions need the paid plan">
          Your plan has changed. Start the paid plan again from your{" "}
          <Link href="/dashboard" className="underline">
            dashboard
          </Link>{" "}
          to use subscriptions.
        </Notice>
      );
    case "gone":
      return (
        <Notice tone="error">
          {mode.kind === "create"
            ? "Subscriptions are not available to your account right now."
            : "This subscription no longer exists."}{" "}
          <Link href={SUBSCRIPTIONS_PATH} className="underline">
            Back to subscriptions
          </Link>
        </Notice>
      );
    case "unavailable":
      return (
        <Notice tone="error">
          The subscription could not be saved right now. Please try again later.
        </Notice>
      );
    default:
      return null;
  }
}

function describedBy(hintId: string, error: string | undefined, field: string) {
  return error === undefined ? hintId : `${hintId} ${field}-error`;
}

function FieldError({
  field,
  errors,
}: {
  field: SubscriptionField;
  errors: FieldErrors;
}) {
  const error = errors[field];
  return error === undefined ? null : (
    <p id={`${field}-error`} className="mt-1 text-sm text-error-ink">
      {error}
    </p>
  );
}

function CreatedPanel({
  state,
  names,
}: {
  state: Extract<FormState, { kind: "created" }>;
  names: ReadonlyMap<string, string>;
}) {
  return (
    <SecretPanel
      title="Subscription added"
      secret={state.secret}
      footer={
        <div className="flex flex-col items-start gap-2 sm:flex-row sm:items-center sm:gap-4">
          <Link href={SUBSCRIPTIONS_PATH} className={BUTTON}>
            Back to subscriptions
          </Link>
          <p className="text-sm text-ink-muted">
            Leaving this page hides the secret for good.
          </p>
        </div>
      }
    >
      {names.get(state.agency) ?? state.agency} will send to{" "}
      <span className="font-mono break-all text-ink">{state.url}</span>.
    </SecretPanel>
  );
}

export function SubscriptionForm({
  mode,
  prompts = false,
}: {
  mode: FormMode;
  prompts?: boolean;
}) {
  const [state, formAction, pending] = useActionState(
    mode.kind === "create" ? createSubscription : updateSubscription,
    initialForm(
      mode.kind === "create"
        ? { agency: mode.agencies[0]?.slug ?? "" }
        : {
            agency: mode.agency,
            url: mode.url,
            format: mode.format,
            prompt: mode.prompt,
          },
    ),
  );

  if (state.kind === "created") {
    const names = new Map(
      mode.kind === "create"
        ? mode.agencies.map((agency) => [agency.slug, agency.name])
        : [],
    );
    return <CreatedPanel state={state} names={names} />;
  }

  const errors = state.kind === "invalid" ? state.errors : {};
  const { values } = state;
  const submitLabel =
    mode.kind === "create" ? "Add subscription" : "Save changes";

  return (
    <div className="max-w-2xl space-y-6">
      <Problem state={state} mode={mode} />
      <form
        key={JSON.stringify(values)}
        action={formAction}
        className="space-y-6"
      >
        {mode.kind === "create" ? (
          <div>
            <label htmlFor="agency" className="font-medium">
              {FIELD_LABELS.agency}
            </label>
            <p id="agency-hint" className="text-sm text-ink-muted">
              You get a webhook each time this agency posts a meeting or an
              agenda.
            </p>
            <select
              id="agency"
              name="agency"
              required
              defaultValue={values.agency}
              aria-invalid={errors.agency === undefined ? undefined : true}
              aria-describedby={describedBy(
                "agency-hint",
                errors.agency,
                "agency",
              )}
              className={INPUT}
            >
              {mode.agencies.map((agency) => (
                <option key={agency.slug} value={agency.slug}>
                  {agency.name}
                </option>
              ))}
            </select>
            <FieldError field="agency" errors={errors} />
          </div>
        ) : (
          <div>
            <input type="hidden" name="id" value={mode.id} />
            <input type="hidden" name="agency" value={mode.agency} />
            <p className="font-medium">{FIELD_LABELS.agency}</p>
            <p className="mt-1">{mode.agencyName}</p>
            <p className="text-sm text-ink-muted">
              The agency cannot be changed. To follow another one, add a new
              subscription.
            </p>
          </div>
        )}
        <div>
          <label htmlFor="url" className="font-medium">
            {FIELD_LABELS.url}
          </label>
          <p id="url-hint" className="text-sm text-ink-muted">
            {URL_HINT}
          </p>
          <input
            id="url"
            name="url"
            type="text"
            inputMode="url"
            required
            maxLength={URL_MAX_LENGTH}
            autoComplete="off"
            spellCheck={false}
            placeholder="https://example.com/hooks"
            defaultValue={values.url}
            aria-invalid={errors.url === undefined ? undefined : true}
            aria-describedby={describedBy("url-hint", errors.url, "url")}
            className={`${INPUT} font-mono`}
          />
          <FieldError field="url" errors={errors} />
        </div>
        <div>
          <label htmlFor="format" className="font-medium">
            {FIELD_LABELS.format}
          </label>
          <p id="format-hint" className="text-sm text-ink-muted">
            {FORMAT_HINT}
          </p>
          <select
            id="format"
            name="format"
            required
            defaultValue={values.format}
            aria-invalid={errors.format === undefined ? undefined : true}
            aria-describedby={describedBy(
              "format-hint",
              errors.format,
              "format",
            )}
            className={INPUT}
          >
            {formatChoices(values.format).map((format) => (
              <option key={format} value={format}>
                {FORMAT_LABELS[format]}
              </option>
            ))}
          </select>
          <FieldError field="format" errors={errors} />
        </div>
        {prompts ? (
          <PromptField
            defaultValue={values.prompt}
            error={errors.prompt}
            editing={mode.kind === "edit"}
          />
        ) : null}
        <div className="flex flex-col items-stretch gap-3 sm:flex-row sm:items-center sm:gap-4">
          <button
            type="submit"
            disabled={pending}
            aria-busy={pending}
            className={BUTTON}
          >
            {pending ? "Saving…" : submitLabel}
          </button>
          <Link
            href={SUBSCRIPTIONS_PATH}
            className="inline-flex min-h-11 items-center justify-center text-sm text-accent underline underline-offset-4"
          >
            Cancel
          </Link>
        </div>
        <p className="text-sm text-ink-muted">
          {mode.kind === "create"
            ? "Its signing secret is shown once, right after you add it."
            : "Changing the URL keeps the same signing secret."}
        </p>
      </form>
    </div>
  );
}
