import Link from "next/link";

import { Section } from "@/components/ui/section";

import { editPath } from "../state";

export function TemplateSummary({
  id,
  agencyName,
  template,
}: {
  id: string;
  agencyName: string;
  template: string;
}) {
  return (
    <Section
      title="Template"
      description="Each webhook's body is this JSON object, with its placeholders filled in from the event."
      actions={
        <Link
          href={`${editPath(id)}#template`}
          aria-label={`Edit the template for ${agencyName}`}
          className="link"
        >
          Edit the template
        </Link>
      }
    >
      <details open className="group">
        <summary className="flex min-h-11 cursor-pointer items-center text-sm font-medium text-accent">
          <span className="group-open:hidden">Show the template</span>
          <span className="hidden group-open:inline">Hide the template</span>
        </summary>
        <pre className="mt-2 overflow-x-auto rounded-md bg-surface-muted p-4 font-mono text-[13px] leading-relaxed">
          {template}
        </pre>
      </details>
    </Section>
  );
}
