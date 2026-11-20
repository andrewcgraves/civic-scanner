import type { ReactNode } from "react";

import { Badge } from "./badge";

export const AI_LABEL = "AI-generated";

export const DEFAULT_AI_NOTE =
  "Written by AI from the agency's own material. It can be wrong or leave things out; the agency's original is the official record.";

export function paragraphsOf(text: string): string[] {
  return text
    .split(/\n+/)
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

export function AiSummary({
  text,
  note = DEFAULT_AI_NOTE,
}: {
  text: string;
  note?: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-ai-line bg-surface p-5 sm:p-6">
      <Badge tone="ai">{AI_LABEL}</Badge>
      <div className="prose mt-4">
        {paragraphsOf(text).map((paragraph, index) => (
          <p key={index}>{paragraph}</p>
        ))}
      </div>
      <p className="mt-4 border-t border-line pt-3 text-sm text-ink-muted">
        {note}
      </p>
    </div>
  );
}
