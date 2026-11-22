import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import {
  FORMATS,
  FORM_ERROR,
  PROMPTS_UNAVAILABLE,
  PROMPT_EXAMPLES,
  PROMPT_INVALID,
  PROMPT_MAX_LENGTH,
  PROMPT_TOO_LONG,
  NEW_SUBSCRIPTION_PATH,
  SUBSCRIPTIONS_PATH,
  SUBSCRIPTIONS_SIGN_IN_PATH,
  agencyName,
  checkPrompt,
  checkUrl,
  checkValues,
  editPath,
  FORMAT_HINTS,
  checkTemplate,
  formatLabel,
  initialForm,
  isSlug,
  isSubscriptionId,
  normalPrompt,
  outcomePath,
  promptLength,
  readId,
  readOutcome,
  readValues,
  readReturnsToSubscription,
  readsPrompt,
  rejection,
  sentence,
  subscriptionOutcomePath,
  subscriptionPath,
} from "./state";
import { STARTER_TEMPLATE, TEMPLATE_REQUIRED } from "./custom-template";

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";

function form(values: Record<string, string | Blob>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(values)) {
    data.append(key, value);
  }
  return data;
}

describe("paths", () => {
  it("lives under the dashboard and signs in back to the list", () => {
    expect(SUBSCRIPTIONS_PATH).toBe("/dashboard/subscriptions");
    expect(NEW_SUBSCRIPTION_PATH).toBe("/dashboard/subscriptions/new");
    expect(SUBSCRIPTIONS_SIGN_IN_PATH).toBe(
      "/auth/sign-in?returnTo=%2Fdashboard%2Fsubscriptions",
    );
    expect(editPath(ID)).toBe(`/dashboard/subscriptions/${ID}/edit`);
    expect(subscriptionPath(ID)).toBe(`/dashboard/subscriptions/${ID}`);
  });

  it("names the outcome on the subscription's own page", () => {
    expect(subscriptionOutcomePath(ID, "resumed", "la-metro")).toBe(
      `/dashboard/subscriptions/${ID}?resumed=la-metro`,
    );
  });

  it("returns to the subscription's page only when the form says so", () => {
    expect(readReturnsToSubscription(form({ returnTo: "subscription" }))).toBe(
      true,
    );
    expect(readReturnsToSubscription(form({ returnTo: "list" }))).toBe(false);
    expect(readReturnsToSubscription(form({}))).toBe(false);
  });

  it("turns text into a sentence", () => {
    expect(sentence(" timed out ")).toBe("Timed out.");
    expect(sentence("Done!")).toBe("Done!");
    expect(sentence("")).toBeUndefined();
    expect(sentence(4)).toBeUndefined();
  });

  it("names the outcome and the agency in the list's query", () => {
    expect(outcomePath("paused", "la-metro")).toBe(
      "/dashboard/subscriptions?paused=la-metro",
    );
  });
});

describe("initialForm", () => {
  it("starts idle with the given values", () => {
    expect(initialForm({ agency: "octa" })).toEqual({
      kind: "idle",
      values: {
        agency: "octa",
        url: "",
        format: "standard",
        template: "",
        prompt: "",
      },
    });
    expect(initialForm({ url: "https://a.example", format: "slack" })).toEqual({
      kind: "idle",
      values: {
        agency: "",
        url: "https://a.example",
        format: "slack",
        template: "",
        prompt: "",
      },
    });
  });
});

describe("readValues and readId", () => {
  it("reads trimmed agency, id and the URL as typed", () => {
    const data = form({
      agency: " octa ",
      url: " https://a.example ",
      format: " discord ",
      template: '{\r\n  "a": "{{agency.name}}"\r\n}',
      prompt: " Fare changes\r\n",
      id: ` ${ID} `,
    });

    expect(readValues(data)).toEqual({
      agency: "octa",
      url: " https://a.example ",
      format: "discord",
      template: '{\n  "a": "{{agency.name}}"\n}',
      prompt: " Fare changes\r\n",
    });
    expect(readsPrompt(data)).toBe(true);
    expect(readId(data)).toBe(ID);
  });

  it("reads missing or file fields as empty, and caps long input", () => {
    const data = form({
      url: "x".repeat(5000),
      agency: new Blob(["a"]),
      template: "y".repeat(40000),
    });

    expect(readValues(data)).toEqual({
      agency: "",
      url: "x".repeat(4096),
      format: "standard",
      template: "y".repeat(32768),
      prompt: "",
    });
    expect(readsPrompt(data)).toBe(false);
    expect(readId(new FormData())).toBe("");
  });
});

describe("isSubscriptionId and isSlug", () => {
  it("accepts lower-case UUIDs only", () => {
    expect(isSubscriptionId(ID)).toBe(true);
    expect(isSubscriptionId(ID.toUpperCase())).toBe(false);
    expect(isSubscriptionId("../admin")).toBe(false);
  });

  it("accepts slugs only", () => {
    expect(isSlug("la-metro")).toBe(true);
    expect(isSlug("La Metro")).toBe(false);
    expect(isSlug("a".repeat(101))).toBe(false);
    expect(isSlug("")).toBe(false);
  });
});

describe("checkUrl and checkValues", () => {
  it("requires a URL of at most 2,048 bytes", () => {
    expect(checkUrl("   ")).toBe("Enter the URL to send webhooks to.");
    expect(checkUrl(`https://a.example/${"é".repeat(1020)}`)).toBe(
      "The URL must be at most 2,048 bytes.",
    );
    expect(checkUrl(" https://a.example/hooks ")).toBeUndefined();
  });

  it("requires an agency slug, a URL and a known format", () => {
    expect(
      checkValues({
        agency: "",
        url: "",
        format: "teams",
        template: "",
        prompt: "x".repeat(501),
      }),
    ).toEqual({
      agency: "Choose an agency.",
      url: "Enter the URL to send webhooks to.",
      format: "Choose a message format.",
      prompt: `${PROMPT_TOO_LONG} Take out 1.`,
    });
    for (const format of FORMATS) {
      expect(
        checkValues({
          agency: "octa",
          url: "https://a.example",
          format,
          template: STARTER_TEMPLATE,
          prompt: "",
        }),
      ).toEqual({});
    }
  });

  it("names each format", () => {
    expect(FORMATS.map(formatLabel)).toEqual([
      "Standard JSON",
      "Discord",
      "Slack",
      "Custom JSON",
    ]);
    expect(formatLabel("teams")).toBe("teams");
  });

  it("describes each format", () => {
    expect(FORMATS.map((format) => FORMAT_HINTS[format])).toEqual([
      "Signed JSON with every field, for your own code.",
      "A ready-made message for a Discord channel.",
      "A ready-made message for a Slack channel.",
      "Your own JSON object, filled in from placeholders.",
    ]);
  });

  it("needs a template of at most 8,192 bytes only for custom", () => {
    const values = {
      agency: "octa",
      url: "https://a.example",
      format: "custom",
      template: "  ",
      prompt: "",
    };
    expect(checkValues(values)).toEqual({ template: TEMPLATE_REQUIRED });
    expect(
      checkTemplate({ ...values, template: `{"a": "${"é".repeat(4096)}"}` }),
    ).toBe("The template is 9 characters too long.");
    expect(checkTemplate({ ...values, format: "slack" })).toBeUndefined();
  });
});

describe("directed prompts", () => {
  it("uses the length limit the API contract states", () => {
    const spec = readFileSync(
      resolve(process.cwd(), "..", "openapi", "v1.yaml"),
      "utf8",
    );
    const schema =
      /\n {4}WebhookPrompt:\n([\s\S]*?)\n {4}\S/.exec(spec)?.[1] ?? "";
    const stated =
      /at most ([\d,]+)\s+characters/.exec(schema.replace(/\s+/g, " "))?.[1] ??
      "";

    expect(stated.replace(",", "")).toBe(String(PROMPT_MAX_LENGTH));
  });

  it("counts characters after trimming, as the API does", () => {
    expect(normalPrompt("  Blue Line\r\nstations \n")).toBe(
      "Blue Line\nstations",
    );
    expect(promptLength("  ab\r\nc  ")).toBe(4);
    expect(promptLength("🚆".repeat(3))).toBe(3);
    expect(promptLength("   ")).toBe(0);
  });

  it("allows up to the limit and says how much to take out past it", () => {
    expect(checkPrompt("")).toBeUndefined();
    expect(checkPrompt(` ${"a".repeat(PROMPT_MAX_LENGTH)} `)).toBeUndefined();
    expect(checkPrompt("🚆".repeat(PROMPT_MAX_LENGTH))).toBeUndefined();
    expect(checkPrompt("a".repeat(PROMPT_MAX_LENGTH + 37))).toBe(
      "The prompt can be at most 500 characters. Take out 37.",
    );
  });

  it("refuses control characters other than line breaks and tabs", () => {
    expect(checkPrompt("Fares\n\tand passes\r\n")).toBeUndefined();
    expect(checkPrompt("Fares\u0007")).toBe(PROMPT_INVALID);
    expect(checkPrompt("Fares\rpasses")).toBe(PROMPT_INVALID);
    expect(checkPrompt("Fares\u0085")).toBe(PROMPT_INVALID);
    expect(checkPrompt("Fares\uFFFD")).toBe(PROMPT_INVALID);
  });

  it("offers examples that each fit the limit", () => {
    expect(PROMPT_EXAMPLES.map((example) => example.label)).toEqual([
      "Fare changes",
      "One rail line",
      "Contracts and votes",
    ]);
    for (const example of PROMPT_EXAMPLES) {
      expect(checkPrompt(example.text)).toBeUndefined();
    }
  });

  it.each([
    ["prompt_too_long", PROMPT_TOO_LONG],
    ["invalid_prompt", PROMPT_INVALID],
    ["prompts_unavailable", PROMPTS_UNAVAILABLE],
  ])("puts %s next to the prompt", (code, message) => {
    expect(rejection({ code, message: "from the API" })).toEqual({
      errors: { prompt: message },
    });
  });

  it("does not take inherited names for prompt codes", () => {
    expect(rejection({ code: "toString", message: "odd" })).toEqual({
      errors: { url: "Odd." },
    });
  });
});

describe("rejection", () => {
  it("puts a template problem next to the template, with where it is", () => {
    const template = '{\n  "a": "x"\n  "b": "y"\n}';
    expect(
      rejection(
        {
          code: "invalid_template_json",
          message:
            "the template is not valid JSON: invalid character '\"' after object key:value pair, at byte 16",
        },
        template,
      ),
    ).toEqual({
      errors: {
        template:
          "Line 3, column 3: The template is not valid JSON: invalid character '\"' after object key:value pair.",
      },
      templateProblem: {
        message:
          "The template is not valid JSON: invalid character '\"' after object key:value pair.",
        index: 15,
      },
    });
    expect(
      rejection({ code: "template_required", message: "x" }).errors,
    ).toEqual({ template: TEMPLATE_REQUIRED });
  });

  it("puts a URL problem next to the URL, as a sentence", () => {
    expect(
      rejection({
        code: "not_public",
        message:
          "the URL's host resolves to an address that is not on the public internet",
      }),
    ).toEqual({
      errors: {
        url: "The URL's host resolves to an address that is not on the public internet.",
      },
    });
    expect(
      rejection({ code: "unsupported_scheme", message: "Use https!" }),
    ).toEqual({ errors: { url: "Use https!" } });
  });

  it("falls back to a general message for a URL problem without text", () => {
    expect(rejection({ code: "invalid_url", message: " " })).toEqual({
      errors: { url: FORM_ERROR },
    });
  });

  it("takes listed field errors first", () => {
    expect(
      rejection({
        code: "invalid_body",
        message: "x",
        fields: [
          "skipped",
          { field: "name", message: "not ours" },
          { field: "url", message: "first" },
          { field: "url", message: "second" },
          { field: "agency", message: 7 },
        ],
      }),
    ).toEqual({ errors: { url: "First." } });
  });

  it("puts an unknown agency next to the agency", () => {
    expect(rejection({ code: "unknown_agency", message: "x" })).toEqual({
      errors: { agency: "Choose an agency from the list." },
    });
  });

  it("puts an unknown format next to the format", () => {
    expect(rejection({ code: "invalid_format", message: "x" })).toEqual({
      errors: { format: "Choose a message format from the list." },
    });
  });

  it.each([
    [{ code: "invalid_body", message: "x" }],
    [{ code: "nothing_to_update", message: "x" }],
    [{ message: "no code" }],
    [{ fields: "not a list" }],
    [null],
    ["text"],
  ])("gives a form-level message for %j", (body) => {
    expect(rejection(body)).toEqual({ errors: {}, message: FORM_ERROR });
  });
});

describe("readOutcome", () => {
  it("reads the first outcome with a valid agency slug", () => {
    expect(readOutcome({ deleted: "octa" })).toEqual({
      kind: "deleted",
      agency: "octa",
    });
    expect(readOutcome({ updated: "Bad Slug", resumed: "rctc" })).toEqual({
      kind: "resumed",
      agency: "rctc",
    });
  });

  it("ignores lists and unknown keys", () => {
    expect(readOutcome({ paused: ["octa", "rctc"] })).toBeNull();
    expect(readOutcome({ created: "octa" })).toBeNull();
  });
});

describe("agencyName", () => {
  it("names a known agency and falls back to the slug", () => {
    const names = new Map([["la-metro", "LA Metro"]]);

    expect(agencyName(names, "la-metro")).toBe("LA Metro");
    expect(agencyName(names, "gone")).toBe("gone");
  });
});
