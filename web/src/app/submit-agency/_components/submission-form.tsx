"use client";

import { useActionState } from "react";

import { Notice } from "@/components/ui/notice";

import { submitAgency } from "../actions";
import {
  FIELD_LABELS,
  INITIAL_STATE,
  NAME_MAX_LENGTH,
  SUBMIT_AGENCY_PATH,
  WEBSITE_MAX_LENGTH,
  type FieldErrors,
  type SubmissionField,
  type SubmissionState,
} from "../state";

const INPUT =
  "mt-1 block w-full rounded-md border border-line-strong bg-surface px-3 py-2 text-ink aria-invalid:border-error-line";

function StatusNotice({ state }: { state: SubmissionState }) {
  switch (state.kind) {
    case "on-file":
      return (
        <Notice tone="error" title="This website is already on file">
          An agency with this website is already tracked or waiting for review,
          so there is no need to suggest it again.
        </Notice>
      );
    case "limit":
      return (
        <Notice tone="error" title="You have reached the limit for now">
          You already have several suggestions waiting for review. You can
          suggest more once they have been reviewed.
        </Notice>
      );
    case "closed":
      return (
        <Notice tone="error">
          Suggestions are not open right now. Please try again later.
        </Notice>
      );
    case "unavailable":
      return (
        <Notice tone="error">
          Your suggestion could not be sent right now. Please try again later.
        </Notice>
      );
    case "invalid":
      return (
        <Notice tone="error" title="Please correct the form">
          {state.message ?? (
            <ul className="list-disc pl-5">
              {(Object.keys(state.errors) as SubmissionField[]).map((field) => (
                <li key={field}>
                  <a href={`#${field}`} className="link">
                    {FIELD_LABELS[field]}
                  </a>
                  : {state.errors[field]}
                </li>
              ))}
            </ul>
          )}
        </Notice>
      );
    default:
      return null;
  }
}

function Field({
  field,
  hint,
  defaultValue,
  errors,
  maxLength,
  autoComplete,
  inputMode,
}: {
  field: SubmissionField;
  hint: string;
  defaultValue: string;
  errors: FieldErrors;
  maxLength: number;
  autoComplete: string;
  inputMode?: "url";
}) {
  const error = errors[field];
  const hintId = `${field}-hint`;
  const errorId = `${field}-error`;
  return (
    <div>
      <label htmlFor={field} className="font-medium">
        {FIELD_LABELS[field]}
      </label>
      <p id={hintId} className="text-sm text-ink-muted">
        {hint}
      </p>
      <input
        id={field}
        name={field}
        type="text"
        required
        maxLength={maxLength}
        autoComplete={autoComplete}
        inputMode={inputMode}
        spellCheck={false}
        defaultValue={defaultValue}
        aria-invalid={error === undefined ? undefined : true}
        aria-describedby={error === undefined ? hintId : `${hintId} ${errorId}`}
        className={INPUT}
      />
      {error === undefined ? null : (
        <p id={errorId} className="mt-1 text-sm text-error-ink">
          {error}
        </p>
      )}
    </div>
  );
}

export function SubmissionForm() {
  const [state, formAction, pending] = useActionState(
    submitAgency,
    INITIAL_STATE,
  );

  if (state.kind === "submitted") {
    return (
      <div className="space-y-4">
        <Notice title="Thanks, your suggestion was sent">
          <p>
            {state.name} ({state.website}) is waiting for review. It will not
            appear on Civic Scanner unless it is approved.
          </p>
        </Notice>
        <p>
          <a href={SUBMIT_AGENCY_PATH} className="link">
            Suggest another agency
          </a>
        </p>
      </div>
    );
  }

  const errors = state.kind === "invalid" ? state.errors : {};
  return (
    <form action={formAction} className="max-w-xl space-y-6">
      <StatusNotice state={state} />
      <Field
        field="name"
        hint="The agency's official name, for example Orange County Transportation Authority."
        defaultValue={state.values.name}
        errors={errors}
        maxLength={NAME_MAX_LENGTH}
        autoComplete="organization"
      />
      <Field
        field="website"
        hint="The agency's own website, for example octa.net."
        defaultValue={state.values.website}
        errors={errors}
        maxLength={WEBSITE_MAX_LENGTH}
        autoComplete="url"
        inputMode="url"
      />
      <button
        type="submit"
        disabled={pending}
        className="rounded-md bg-accent px-4 py-2 font-medium text-canvas hover:bg-accent-strong disabled:opacity-60"
      >
        {pending ? "Sending…" : "Suggest this agency"}
      </button>
    </form>
  );
}
