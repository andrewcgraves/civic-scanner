"use client";

import { useEffect, useRef, useState, type RefObject } from "react";

import { Notice } from "@/components/ui/notice";

import {
  DEFAULT_FORMAT,
  FIELD_LABELS,
  FORMATS,
  FORMAT_HINTS,
  FORMAT_LABELS,
  isFormat,
  type SubscriptionFormat,
} from "../state";
import { STARTER_TEMPLATE, type TemplateProblem } from "../custom-template";

import { TemplateEditor } from "./template-editor";
import { TemplatePreview, usePreview } from "./template-preview";

export interface AgencyName {
  slug: string;
  name: string;
}

const CHOICE =
  "flex min-h-11 cursor-pointer items-start gap-3 rounded-lg bg-surface";
const CHOSEN = `${CHOICE} border-2 border-accent bg-scheduled-surface px-[15px] py-[11px]`;
const UNCHOSEN = `${CHOICE} border border-line-strong px-4 py-3 hover:bg-surface-muted`;

function useAgency(
  anchor: RefObject<HTMLFieldSetElement | null>,
  initial: string,
) {
  const [agency, setAgency] = useState(initial);
  useEffect(() => {
    const form = anchor.current?.form;
    if (form === null || form === undefined) {
      return undefined;
    }
    const listen = (event: Event) => {
      const target = event.target;
      if (target instanceof HTMLSelectElement && target.name === "agency") {
        setAgency(target.value);
      }
    };
    form.addEventListener("change", listen);
    return () => {
      form.removeEventListener("change", listen);
    };
  }, [anchor]);
  return agency;
}

export function FormatField({
  defaultFormat,
  defaultTemplate,
  savedProblem,
  error,
  agency: initialAgency,
  agencies,
  keepsTemplate,
}: {
  defaultFormat: string;
  defaultTemplate: string;
  savedProblem: TemplateProblem | undefined;
  error: string | undefined;
  agency: string;
  agencies: readonly AgencyName[];
  keepsTemplate: boolean;
}) {
  const anchor = useRef<HTMLFieldSetElement>(null);
  const [format, setFormat] = useState<SubscriptionFormat>(
    isFormat(defaultFormat) ? defaultFormat : DEFAULT_FORMAT,
  );
  const [template, setTemplate] = useState(defaultTemplate);
  const agency = useAgency(anchor, initialAgency);
  const custom = format === "custom";
  const preview = usePreview(custom ? template : "", agency, {
    template: defaultTemplate,
    problem: savedProblem,
  });
  const agencyName =
    agencies.find((option) => option.slug === agency)?.name ?? "Sample agency";
  const describedBy =
    error === undefined ? "format-hint" : "format-hint format-error";

  function choose(next: SubscriptionFormat) {
    setFormat(next);
    if (next === "custom" && template.trim() === "") {
      setTemplate(STARTER_TEMPLATE);
    }
  }

  return (
    <>
      <fieldset
        ref={anchor}
        id="format"
        aria-describedby={describedBy}
        aria-invalid={error === undefined ? undefined : true}
        className="min-w-0"
      >
        <legend className="font-medium">{FIELD_LABELS.format}</legend>
        <p id="format-hint" className="text-sm text-ink-muted">
          Every format is signed the same way. Discord and Slack go to a
          channel&apos;s incoming webhook URL.
        </p>
        <div className="mt-2 grid gap-2 sm:grid-cols-2">
          {FORMATS.map((option) => (
            <label
              key={option}
              className={option === format ? CHOSEN : UNCHOSEN}
            >
              <input
                type="radio"
                name="format"
                value={option}
                checked={option === format}
                onChange={() => {
                  choose(option);
                }}
                className="mt-0.5 size-[18px] shrink-0 accent-accent"
              />
              <span className="flex flex-col gap-0.5">
                <span className="font-medium">{FORMAT_LABELS[option]}</span>{" "}
                <span className="text-sm text-ink-muted">
                  {FORMAT_HINTS[option]}
                </span>
              </span>
            </label>
          ))}
        </div>
        {error === undefined ? null : (
          <p id="format-error" className="mt-1 text-sm text-error-ink">
            {error}
          </p>
        )}
      </fieldset>
      {custom ? (
        <>
          <TemplateEditor
            value={template}
            onChange={setTemplate}
            problem={preview.problem}
          />
          <TemplatePreview preview={preview} agencyName={agencyName} />
        </>
      ) : keepsTemplate && defaultTemplate.trim() !== "" ? (
        <Notice>
          Your custom template is kept. Choose {FORMAT_LABELS.custom} again to
          use it.
        </Notice>
      ) : null}
    </>
  );
}
