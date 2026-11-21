"use client";

import { useEffect, useId, useState } from "react";

import { Badge } from "@/components/ui/badge";

import { previewTemplate } from "../actions";
import { DEFAULT_EVENT_TYPE, EVENT_LABELS, EVENT_TYPES } from "../deliveries";
import {
  localProblem,
  prettyBody,
  token,
  type PreviewEventType,
  type PreviewState,
  type TemplateProblem,
} from "../template";

import { BUTTON, INPUT } from "./styles";

export const PREVIEW_DELAY_MS = 500;

export type PreviewView =
  PreviewState | { kind: "checking"; body: string | undefined };

interface Answer {
  key: string;
  state: PreviewState;
}

export interface Preview {
  view: PreviewView;
  problem: TemplateProblem | undefined;
  type: PreviewEventType;
  setType: (type: PreviewEventType) => void;
  retry: () => void;
}

export function usePreview(
  template: string,
  agency: string,
  saved: { template: string; problem: TemplateProblem | undefined },
): Preview {
  const [type, setType] = useState<PreviewEventType>(DEFAULT_EVENT_TYPE);
  const [attempt, setAttempt] = useState(0);
  const [answer, setAnswer] = useState<Answer | null>(null);
  const local = localProblem(template);
  const blocked = local !== undefined;
  const key = JSON.stringify([template, type, agency, attempt]);

  useEffect(() => {
    if (blocked) {
      return undefined;
    }
    let live = true;
    const timer = setTimeout(() => {
      previewTemplate({ template, type, agency }).then(
        (state) => {
          if (live) {
            setAnswer({ key, state });
          }
        },
        () => {
          if (live) {
            setAnswer({ key, state: { kind: "unavailable" } });
          }
        },
      );
    }, PREVIEW_DELAY_MS);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [key, blocked, template, type, agency]);

  const retry = () => {
    setAttempt((count) => count + 1);
  };

  if (local !== undefined) {
    return {
      view: { kind: "invalid", problem: local },
      problem: local,
      type,
      setType,
      retry,
    };
  }
  if (answer?.key === key) {
    return {
      view: answer.state,
      problem:
        answer.state.kind === "invalid" ? answer.state.problem : undefined,
      type,
      setType,
      retry,
    };
  }
  const earlier = answer?.state;
  const savedProblem = template === saved.template ? saved.problem : undefined;
  return {
    view: {
      kind: "checking",
      body: earlier?.kind === "rendered" ? earlier.body : undefined,
    },
    problem: earlier?.kind === "invalid" ? earlier.problem : savedProblem,
    type,
    setType,
    retry,
  };
}

function Status({
  view,
  agencyName,
}: {
  view: PreviewView;
  agencyName: string;
}) {
  switch (view.kind) {
    case "rendered":
      return (
        <>
          <Badge tone="held">Valid</Badge>{" "}
          <span className="text-ink-muted">
            Rendered for {agencyName}, marked as a test.
          </span>
        </>
      );
    case "invalid":
      return (
        <>
          <Badge tone="cancelled">Not valid</Badge>{" "}
          <span className="text-ink-muted">
            Fix the template to see the preview.
          </span>
        </>
      );
    case "unavailable":
      return (
        <>
          <Badge>Unavailable</Badge>{" "}
          <span className="text-ink-muted">
            The preview cannot be shown right now. The template is still checked
            when you save.
          </span>
        </>
      );
    default:
      return <Badge>Checking…</Badge>;
  }
}

export function TemplatePreview({
  preview,
  agencyName,
}: {
  preview: Preview;
  agencyName: string;
}) {
  const typeId = useId();
  const headingId = useId();
  const { view } = preview;
  const body =
    view.kind === "rendered"
      ? view.body
      : view.kind === "checking"
        ? view.body
        : undefined;

  return (
    <section
      aria-labelledby={headingId}
      className="space-y-3 rounded-lg border border-line bg-surface px-5 py-4"
    >
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id={headingId} className="text-lg font-semibold">
            Preview
          </h2>
          <p className="text-sm text-ink-muted">
            The body a sample event would send, updated as you type. Nothing is
            sent.
          </p>
        </div>
        <div className="min-w-56">
          <label htmlFor={typeId} className="text-sm font-medium">
            Sample event
          </label>
          <select
            id={typeId}
            value={preview.type}
            onChange={(event) => {
              const chosen = EVENT_TYPES.find(
                (type) => type === event.target.value,
              );
              if (chosen !== undefined) {
                preview.setType(chosen);
              }
            }}
            className={INPUT}
          >
            {EVENT_TYPES.map((type) => (
              <option key={type} value={type}>
                {EVENT_LABELS[type]}
              </option>
            ))}
          </select>
        </div>
      </div>
      <p role="status" className="flex flex-wrap items-center gap-2 text-sm">
        <Status view={view} agencyName={agencyName} />
      </p>
      {view.kind === "unavailable" ? (
        <button
          type="button"
          onClick={preview.retry}
          className={`${BUTTON} text-sm`}
        >
          Try again
        </button>
      ) : null}
      {body === undefined ? null : (
        <pre
          aria-label="Rendered body"
          className={`overflow-x-auto rounded-md bg-surface-muted p-4 font-mono text-[13px] leading-relaxed ${view.kind === "checking" ? "opacity-60" : ""}`}
        >
          {prettyBody(body)}
        </pre>
      )}
      <p className="text-sm text-ink-muted">
        Sent compact, on one line, with the keys in your order; shown formatted
        here. Placeholders become text, so{" "}
        <code className="font-mono">&quot;{token("event.test")}&quot;</code>{" "}
        sends the string <code className="font-mono">&quot;true&quot;</code>.
      </p>
    </section>
  );
}
