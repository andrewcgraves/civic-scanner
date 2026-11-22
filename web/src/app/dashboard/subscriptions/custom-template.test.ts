import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import {
  PLACEHOLDER_NAMES,
  STARTER_TEMPLATE,
  TEMPLATE_MAX_BYTES,
  TEMPLATE_REQUIRED,
  closestPlaceholder,
  excerpt,
  indexOfByte,
  isTemplateCode,
  localProblem,
  locate,
  normalTemplate,
  prettyBody,
  problemText,
  sizeLabel,
  templateBytes,
  templateProblem,
  token,
} from "./custom-template";

function specSection(name: string): string {
  const spec = readFileSync(
    resolve(process.cwd(), "..", "openapi", "v1.yaml"),
    "utf8",
  );
  const start = spec.indexOf(`    ${name}:\n`);
  const end = spec.indexOf("\n    WebhookTemplatePreviewInput:", start);
  return spec.slice(start, end);
}

describe("placeholders", () => {
  it("lists exactly the placeholders the API contract names", () => {
    const section = specSection("WebhookTemplate");
    const named = [...section.matchAll(/`([a-z]+\.[A-Za-z]+)`/g)].map(
      (match) => match[1],
    );
    expect([...PLACEHOLDER_NAMES].sort()).toEqual([...new Set(named)].sort());
    expect(section).toContain(
      `at most ${TEMPLATE_MAX_BYTES.toLocaleString("en-US")} bytes`,
    );
  });

  it("starts from an example that uses only known placeholders", () => {
    const used = [...STARTER_TEMPLATE.matchAll(/\{\{(.*?)\}\}/g)].map(
      (match) => match[1],
    );
    expect(used.length).toBeGreaterThan(0);
    for (const name of used) {
      expect(PLACEHOLDER_NAMES).toContain(name);
    }
    expect(() => {
      JSON.parse(STARTER_TEMPLATE);
    }).not.toThrow();
  });

  it("suggests the one placeholder close to a typo", () => {
    expect(closestPlaceholder("meeting.titel")).toBe("meeting.title");
    expect(closestPlaceholder(" Agency.Name ")).toBe("agency.name");
    expect(closestPlaceholder("weather")).toBeUndefined();
    expect(closestPlaceholder("event.i")).toBe("event.id");
  });

  it("writes a placeholder in braces", () => {
    expect(token("event.id")).toBe("{{event.id}}");
  });
});

describe("size", () => {
  it("counts UTF-8 bytes and says how many of the limit are used", () => {
    expect(templateBytes("é")).toBe(2);
    expect(sizeLabel(291)).toBe("291 of 8,192 characters used");
  });

  it("needs a template, and one within the limit", () => {
    expect(localProblem(" \n ")).toEqual({ message: TEMPLATE_REQUIRED });
    expect(localProblem("x".repeat(TEMPLATE_MAX_BYTES))).toBeUndefined();
    expect(localProblem("x".repeat(TEMPLATE_MAX_BYTES + 1))).toEqual({
      message: "The template is 1 character too long.",
    });
    expect(localProblem("x".repeat(TEMPLATE_MAX_BYTES + 210))).toEqual({
      message: "The template is 210 characters too long.",
    });
  });

  it("turns Windows line breaks into plain ones", () => {
    expect(normalTemplate("{\r\n}\r\n")).toBe("{\n}\n");
  });
});

describe("positions", () => {
  it("maps a byte offset to a string index", () => {
    expect(indexOfByte("abc", 1)).toBe(1);
    expect(indexOfByte("éa", 2)).toBe(1);
    expect(indexOfByte("😀a", 4)).toBe(2);
    expect(indexOfByte("ab", 9)).toBe(2);
  });

  it("finds the line and column, and quotes the line with a marker", () => {
    const text = '{\n  "a": 1\n  "b": 2\n}';
    const location = locate(text, text.indexOf('"b"'));
    expect(location).toEqual({ line: 3, column: 3, text: '  "b": 2' });
    expect(excerpt(location)).toBe('3 |   "b": 2\n  |   ^');
    expect(locate("ab", 0)).toEqual({ line: 1, column: 1, text: "ab" });
  });
});

describe("templateProblem", () => {
  const broken = '{\n  "a": "x"\n  "b": 1\n}';

  it("points a JSON syntax error at the character before its offset", () => {
    const problem = templateProblem(
      "invalid_template_json",
      "the template is not valid JSON: invalid character '\"' after object key:value pair, at byte 16",
      broken,
    );
    expect(problem).toEqual({
      message:
        "The template is not valid JSON: invalid character '\"' after object key:value pair.",
      index: 15,
    });
    expect(problemText(problem ?? { message: "" }, broken)).toMatch(
      /^Line 3, column 3: /,
    );
  });

  it("points data after the object at its first character", () => {
    const text = '{"a": 1}\n  x';
    expect(
      templateProblem(
        "invalid_template_json",
        "the template has data after the JSON object, at byte 8",
        text,
      ),
    ).toEqual({
      message: "The template has data after the JSON object.",
      index: 11,
    });
  });

  it("names an unknown placeholder, where it is and a likely fix", () => {
    const text = '{"title": "{{meeting.titel}}"}';
    expect(
      templateProblem(
        "unknown_placeholder",
        "unknown placeholder {{meeting.titel}} in `title`; the placeholders are agency.name, agency.slug",
        text,
      ),
    ).toEqual({
      message: "{{meeting.titel}} in “title” is not a placeholder.",
      placeholder: "{{meeting.titel}}",
      index: 11,
      suggestion: "{{meeting.title}}",
    });
  });

  it("uses the API's sentence for other template problems", () => {
    expect(
      templateProblem(
        "invalid_template",
        "the key `title` appears more than once",
        "{}",
      ),
    ).toEqual({ message: "The key “title” appears more than once." });
    expect(templateProblem("template_required", "", "")).toEqual({
      message: TEMPLATE_REQUIRED,
    });
    expect(templateProblem("template_too_large", "", "x".repeat(8200))).toEqual(
      { message: "The template is 8 characters too long." },
    );
    expect(templateProblem("not_public", "x", "{}")).toBeUndefined();
  });

  it("knows the template codes", () => {
    expect(isTemplateCode("unknown_placeholder")).toBe(true);
    expect(isTemplateCode("invalid_format")).toBe(false);
  });
});

describe("prettyBody", () => {
  it("indents a compact body without changing its values", () => {
    expect(
      prettyBody('{"a":"x, {y}: \\"z\\"","b":1.50,"c":[],"d":{"e":[1,2]}}'),
    ).toBe(`{
  "a": "x, {y}: \\"z\\"",
  "b": 1.50,
  "c": [],
  "d": {
    "e": [
      1,
      2
    ]
  }
}`);
  });
});
