"use client";

import { useLayoutEffect, useRef, useState } from "react";

import { FIELD_LABELS } from "../state";
import {
  PLACEHOLDER_GROUPS,
  TEMPLATE_MAX_BYTES,
  excerpt,
  locate,
  sizeLabel,
  templateBytes,
  token,
  type TemplateProblem,
} from "../custom-template";

import { BUTTON, INPUT } from "./styles";

const TAG =
  "inline-flex h-7 items-center rounded-md bg-surface-muted px-2 font-mono text-xs text-accent hover:bg-scheduled-surface hover:text-accent-strong";
const SMALL_BUTTON = `${BUTTON} text-sm`;

interface Selection {
  start: number;
  end: number;
}

function ProblemDetail({
  problem,
  value,
  onSelect,
  onReplace,
}: {
  problem: TemplateProblem;
  value: string;
  onSelect: (selection: Selection) => void;
  onReplace: (from: string, to: string) => void;
}) {
  const { placeholder, suggestion } = problem;
  if (placeholder !== undefined) {
    const found = value.indexOf(placeholder);
    return (
      <>
        <p>
          {problem.message}
          {suggestion === undefined ? null : (
            <>
              {" "}
              Did you mean <code className="font-mono">{suggestion}</code>?
            </>
          )}
        </p>
        {found === -1 ? null : (
          <div className="mt-2 flex flex-wrap gap-2">
            {suggestion === undefined ? null : (
              <button
                type="button"
                className={SMALL_BUTTON}
                onClick={() => {
                  onReplace(placeholder, suggestion);
                }}
              >
                Use {suggestion}
              </button>
            )}
            <button
              type="button"
              className={SMALL_BUTTON}
              onClick={() => {
                onSelect({ start: found, end: found + placeholder.length });
              }}
            >
              Select it
            </button>
          </div>
        )}
      </>
    );
  }
  if (problem.index === undefined) {
    return <p>{problem.message}</p>;
  }
  const index = Math.min(problem.index, value.length);
  const location = locate(value, index);
  return (
    <>
      <p>
        <span className="font-medium">
          Line {location.line}, column {location.column}:
        </span>{" "}
        {problem.message}
      </p>
      <pre
        aria-hidden="true"
        className="mt-2 overflow-x-auto rounded-md border border-error-line bg-surface px-3 py-2 font-mono text-[13px] leading-normal text-ink"
      >
        {excerpt(location)}
      </pre>
      <div className="mt-2">
        <button
          type="button"
          className={SMALL_BUTTON}
          onClick={() => {
            onSelect({
              start: index,
              end: Math.min(index + 1, value.length),
            });
          }}
        >
          Go to line {location.line}
        </button>
      </div>
    </>
  );
}

export function TemplateEditor({
  value,
  onChange,
  problem,
}: {
  value: string;
  onChange: (value: string) => void;
  problem: TemplateProblem | undefined;
}) {
  const field = useRef<HTMLTextAreaElement>(null);
  const [selection, setSelection] = useState<Selection | null>(null);
  const bytes = templateBytes(value);
  const over = bytes > TEMPLATE_MAX_BYTES;
  const describedBy = [
    "template-hint",
    "template-size",
    ...(problem === undefined ? [] : ["template-error"]),
  ].join(" ");

  useLayoutEffect(() => {
    const element = field.current;
    if (selection === null || element === null) {
      return;
    }
    element.focus();
    element.setSelectionRange(selection.start, selection.end);
  }, [selection]);

  function replaceRange(start: number, end: number, text: string) {
    onChange(`${value.slice(0, start)}${text}${value.slice(end)}`);
    const caret = start + text.length;
    setSelection({ start: caret, end: caret });
  }

  function insert(name: string) {
    const element = field.current;
    const start = element?.selectionStart ?? value.length;
    const end = element?.selectionEnd ?? value.length;
    replaceRange(start, end, token(name));
  }

  function replace(from: string, to: string) {
    const start = value.indexOf(from);
    if (start !== -1) {
      replaceRange(start, start + from.length, to);
    }
  }

  return (
    <div>
      <label htmlFor="template" className="font-medium">
        {FIELD_LABELS.template}
      </label>
      <p id="template-hint" className="max-w-[65ch] text-sm text-ink-muted">
        A JSON object of up to 8,192 characters, sent as each webhook&apos;s
        body. Put placeholders such as{" "}
        <code className="font-mono">{token("meeting.title")}</code> inside
        string values; each is replaced with the event&apos;s text.
      </p>
      <textarea
        ref={field}
        id="template"
        name="template"
        rows={11}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        autoCorrect="off"
        aria-invalid={problem === undefined ? undefined : true}
        aria-describedby={describedBy}
        className={`${INPUT} mt-2 resize-y font-mono text-sm leading-relaxed`}
      />
      <div className="mt-2 flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div
          role="group"
          aria-label="Insert a placeholder at the cursor"
          className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5"
        >
          {PLACEHOLDER_GROUPS.map((group, index) => (
            <span key={group.name} className="contents">
              <span
                className={`pr-0.5 text-xs font-semibold text-ink-muted ${index === 0 ? "" : "ml-3"}`}
              >
                {group.name}
              </span>
              {group.placeholders.map((placeholder) => (
                <button
                  key={placeholder.name}
                  type="button"
                  title={placeholder.hint}
                  aria-label={`Insert ${token(placeholder.name)}`}
                  onClick={() => {
                    insert(placeholder.name);
                  }}
                  className={TAG}
                >
                  {placeholder.name}
                </button>
              ))}
            </span>
          ))}
        </div>
        <p
          id="template-size"
          className={`text-sm whitespace-nowrap tabular-nums ${over ? "font-medium text-error-ink" : "text-ink-muted"}`}
        >
          {sizeLabel(bytes)}
        </p>
      </div>
      {problem === undefined ? null : (
        <div id="template-error" className="mt-2 text-sm text-error-ink">
          <ProblemDetail
            problem={problem}
            value={value}
            onSelect={setSelection}
            onReplace={replace}
          />
        </div>
      )}
    </div>
  );
}
