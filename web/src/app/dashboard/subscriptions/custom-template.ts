import type { components } from "@/lib/api/v1";

export type PreviewEventType = components["schemas"]["WebhookEventType"];

export const TEMPLATE_MAX_BYTES = 8192;

export const TEMPLATE_REQUIRED =
  "Write a template, or choose another message format.";

const MESSAGE_LIMIT = 300;
const EXCERPT_LIMIT = 120;

export interface Placeholder {
  name: string;
  hint: string;
}

export interface PlaceholderGroup {
  name: string;
  placeholders: readonly Placeholder[];
}

export const PLACEHOLDER_GROUPS: readonly PlaceholderGroup[] = [
  {
    name: "Agency",
    placeholders: [
      { name: "agency.name", hint: "Its name" },
      { name: "agency.slug", hint: "Its short id, as in its page address" },
    ],
  },
  {
    name: "Meeting",
    placeholders: [
      { name: "meeting.title", hint: "Its title" },
      { name: "meeting.startsAt", hint: "When it starts, in UTC" },
      {
        name: "meeting.link",
        hint: "Its Civic Scanner page; empty when there is none",
      },
      {
        name: "meeting.sourceUrl",
        hint: "The agency's page for it; empty when unknown",
      },
      { name: "meeting.id", hint: "Its id" },
    ],
  },
  {
    name: "Event",
    placeholders: [
      { name: "event.label", hint: "Such as Agenda changed" },
      { name: "event.summary", hint: "One sentence about what happened" },
      { name: "event.type", hint: "Such as agenda_changed" },
      { name: "event.occurredAt", hint: "When it happened, in UTC" },
      { name: "event.test", hint: "true for a test, otherwise false" },
      { name: "event.id", hint: "Its id" },
    ],
  },
  {
    name: "Agenda",
    placeholders: [
      {
        name: "agenda.version",
        hint: "Its version; empty for a transcript",
      },
      {
        name: "agenda.changeNote",
        hint: "The agency's note on the change; empty when there is none",
      },
    ],
  },
];

export const PLACEHOLDER_NAMES: readonly string[] = PLACEHOLDER_GROUPS.flatMap(
  (group) => group.placeholders.map((placeholder) => placeholder.name),
);

export function token(name: string): string {
  return `{{${name}}}`;
}

export const STARTER_TEMPLATE = `{
  "title": "${token("event.label")}: ${token("meeting.title")}",
  "agency": "${token("agency.name")}",
  "summary": "${token("event.summary")}",
  "startsAt": "${token("meeting.startsAt")}",
  "page": "${token("meeting.link")}",
  "test": "${token("event.test")}"
}`;

export interface TemplateProblem {
  message: string;
  index?: number;
  placeholder?: string;
  suggestion?: string;
}

const encoder = new TextEncoder();

export function templateBytes(template: string): number {
  return encoder.encode(template).length;
}

export function normalTemplate(template: string): string {
  return template.replace(/\r\n/g, "\n");
}

export function sizeLabel(bytes: number): string {
  return `${bytes.toLocaleString("en-US")} of ${TEMPLATE_MAX_BYTES.toLocaleString("en-US")} characters used`;
}

function tooLarge(bytes: number): TemplateProblem {
  const over = bytes - TEMPLATE_MAX_BYTES;
  return {
    message: `The template is ${over.toLocaleString("en-US")} ${over === 1 ? "character" : "characters"} too long.`,
  };
}

export function localProblem(template: string): TemplateProblem | undefined {
  if (template.trim() === "") {
    return { message: TEMPLATE_REQUIRED };
  }
  const bytes = templateBytes(template);
  return bytes > TEMPLATE_MAX_BYTES ? tooLarge(bytes) : undefined;
}

export function indexOfByte(text: string, byte: number): number {
  let bytes = 0;
  let index = 0;
  for (const char of text) {
    if (bytes >= byte) {
      return index;
    }
    bytes += encoder.encode(char).length;
    index += char.length;
  }
  return text.length;
}

export interface Location {
  line: number;
  column: number;
  text: string;
}

export function locate(text: string, index: number): Location {
  const at = Math.min(Math.max(index, 0), text.length);
  const start = text.lastIndexOf("\n", at - 1) + 1;
  const end = text.indexOf("\n", at);
  const before = text.slice(0, start);
  return {
    line: before.split("\n").length,
    column: [...text.slice(start, at)].length + 1,
    text: text.slice(start, end === -1 ? undefined : end),
  };
}

export function excerpt(location: Location): string {
  const line = String(location.line);
  const shown = [...location.text].slice(0, EXCERPT_LIMIT).join("");
  const column = Math.min(location.column, EXCERPT_LIMIT + 1);
  return `${line} | ${shown}\n${" ".repeat(line.length)} | ${" ".repeat(column - 1)}^`;
}

function distance(a: string, b: string): number {
  let row = Array.from({ length: b.length + 1 }, (_, j) => j);
  for (const [i, left] of [...a].entries()) {
    const next = [i + 1];
    for (const [j, right] of [...b].entries()) {
      next.push(
        Math.min(
          (row[j + 1] ?? 0) + 1,
          (next[j] ?? 0) + 1,
          (row[j] ?? 0) + (left === right ? 0 : 1),
        ),
      );
    }
    row = next;
  }
  return row[b.length] ?? 0;
}

export function closestPlaceholder(name: string): string | undefined {
  const wanted = name.trim().toLowerCase();
  const scored = PLACEHOLDER_NAMES.map((candidate) => ({
    candidate,
    score: distance(wanted, candidate.toLowerCase()),
  })).sort((a, b) => a.score - b.score);
  const [best, next] = scored;
  if (best === undefined || best.score > 3 || next?.score === best.score) {
    return undefined;
  }
  return best.candidate;
}

function quoted(text: string): string {
  return text.replace(/`([^`]*)`/g, "\u201c$1\u201d");
}

function sentence(text: string): string {
  const trimmed = quoted(text.trim()).slice(0, MESSAGE_LIMIT);
  const capital = `${trimmed.charAt(0).toUpperCase()}${trimmed.slice(1)}`;
  return /[.!?]$/.test(capital) ? capital : `${capital}.`;
}

const AT_BYTE = /,? at byte (\d+)$/;
const UNKNOWN = /^unknown placeholder \{\{(.*?)\}\} in (.+?);/;

function jsonProblem(message: string, template: string): TemplateProblem {
  const at = AT_BYTE.exec(message);
  const text = sentence(message.replace(AT_BYTE, ""));
  if (at === null) {
    return { message: text };
  }
  const byte = Number(at[1]);
  const syntax = message.startsWith("the template is not valid JSON:");
  const index = indexOfByte(template, syntax ? Math.max(byte - 1, 0) : byte);
  const skipped = template.slice(index).search(/\S/);
  return {
    message: text,
    index: syntax || skipped === -1 ? index : index + skipped,
  };
}

function placeholderProblem(
  message: string,
  template: string,
): TemplateProblem {
  const unknown = UNKNOWN.exec(message);
  if (unknown === null) {
    return { message: sentence(message) };
  }
  const raw = unknown[1] ?? "";
  const where = unknown[2] ?? "";
  const placeholder = token(raw);
  const found = template.indexOf(placeholder);
  const suggestion = closestPlaceholder(raw);
  return {
    message: `${placeholder} in ${quoted(where)} is not a placeholder.`,
    placeholder,
    ...(found === -1 ? {} : { index: found }),
    ...(suggestion === undefined ? {} : { suggestion: token(suggestion) }),
  };
}

export const TEMPLATE_CODES = [
  "template_required",
  "template_too_large",
  "invalid_template_json",
  "invalid_template",
  "unknown_placeholder",
] as const;

export function isTemplateCode(code: unknown): boolean {
  return TEMPLATE_CODES.includes(code as (typeof TEMPLATE_CODES)[number]);
}

export function templateProblem(
  code: unknown,
  message: unknown,
  template: string,
): TemplateProblem | undefined {
  const text = typeof message === "string" ? message.trim() : "";
  switch (code) {
    case "template_required":
      return { message: TEMPLATE_REQUIRED };
    case "template_too_large":
      return tooLarge(
        Math.max(templateBytes(template), TEMPLATE_MAX_BYTES + 1),
      );
    case "invalid_template_json":
      return jsonProblem(text, template);
    case "unknown_placeholder":
      return placeholderProblem(text, template);
    case "invalid_template":
      return { message: text === "" ? "Check the template." : sentence(text) };
    default:
      return undefined;
  }
}

export type PreviewState =
  | { kind: "rendered"; type: PreviewEventType; body: string }
  | { kind: "invalid"; problem: TemplateProblem }
  | { kind: "unavailable" };

export function prettyBody(body: string): string {
  let out = "";
  let depth = 0;
  let inString = false;
  let escaped = false;
  const indent = () => `\n${"  ".repeat(depth)}`;
  for (let i = 0; i < body.length; i += 1) {
    const char = body.charAt(i);
    if (inString) {
      out += char;
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === '"') {
        inString = false;
      }
      continue;
    }
    if (char === '"') {
      inString = true;
      out += char;
    } else if (char === "{" || char === "[") {
      const close = char === "{" ? "}" : "]";
      if (body[i + 1] === close) {
        out += char + close;
        i += 1;
      } else {
        depth += 1;
        out += char + indent();
      }
    } else if (char === "}" || char === "]") {
      depth = Math.max(depth - 1, 0);
      out += indent() + char;
    } else if (char === ",") {
      out += `,${indent()}`;
    } else if (char === ":") {
      out += ": ";
    } else if (!/\s/.test(char)) {
      out += char;
    }
  }
  return out;
}

export function problemText(problem: TemplateProblem, template: string) {
  if (problem.index === undefined || problem.placeholder !== undefined) {
    return problem.message;
  }
  const { line, column } = locate(template, problem.index);
  return `Line ${String(line)}, column ${String(column)}: ${problem.message}`;
}
