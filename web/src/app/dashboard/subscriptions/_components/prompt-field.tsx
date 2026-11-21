"use client";

import { useRef, useState } from "react";

import {
  FIELD_LABELS,
  PROMPT_EXAMPLES,
  PROMPT_MAX_LENGTH,
  promptLength,
} from "../state";

import { INPUT } from "./styles";

const CHIP =
  "inline-flex min-h-11 items-center rounded-md border border-line-strong bg-surface px-3 py-1.5 text-sm text-ink hover:bg-surface-muted";

export function PromptField({
  defaultValue,
  error,
  editing,
}: {
  defaultValue: string;
  error: string | undefined;
  editing: boolean;
}) {
  const [value, setValue] = useState(defaultValue);
  const field = useRef<HTMLTextAreaElement>(null);
  const length = promptLength(value);
  const over = length > PROMPT_MAX_LENGTH;
  const empty = value.trim() === "";
  const describedBy = [
    "prompt-hint",
    "prompt-count",
    ...(error === undefined ? [] : ["prompt-error"]),
  ].join(" ");

  function fillWith(text: string) {
    if (field.current !== null) {
      field.current.value = text;
      field.current.focus();
    }
    setValue(text);
  }

  return (
    <div>
      <label htmlFor="prompt" className="font-medium">
        {FIELD_LABELS.prompt}{" "}
        <span className="font-normal text-ink-muted">(optional)</span>
      </label>
      <p id="prompt-hint" className="max-w-[65ch] text-sm text-ink-muted">
        Say what you want to hear about. Each agenda and transcript is read
        against it, and the webhook carries a short message about just that.
        When nothing in a meeting matches, nothing is sent.{" "}
        {editing
          ? "Clear it to get every event again."
          : "Leave it empty to get every event."}
      </p>
      <textarea
        ref={field}
        id="prompt"
        name="prompt"
        rows={editing && !empty ? 6 : 4}
        defaultValue={defaultValue}
        onChange={(event) => {
          setValue(event.target.value);
        }}
        placeholder="For example: fare changes, or anything about the Blue Line"
        aria-invalid={error === undefined ? undefined : true}
        aria-describedby={describedBy}
        className={`${INPUT} resize-y leading-relaxed`}
      />
      <div className="mt-2 flex flex-wrap items-start justify-between gap-x-4 gap-y-1">
        {error === undefined ? null : (
          <p id="prompt-error" className="text-sm text-error-ink">
            {error}
          </p>
        )}
        {empty ? (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-ink-muted">
              Start from an example:
            </span>
            {PROMPT_EXAMPLES.map((example) => (
              <button
                key={example.label}
                type="button"
                onClick={() => {
                  fillWith(example.text);
                }}
                className={CHIP}
              >
                {example.label}
              </button>
            ))}
          </div>
        ) : null}
        <p
          id="prompt-count"
          className={`flex min-h-11 items-center text-sm tabular-nums ${over ? "font-medium text-error-ink" : "text-ink-muted"}`}
        >
          {`${String(length)} / ${String(PROMPT_MAX_LENGTH)} characters`}
        </p>
      </div>
    </div>
  );
}
