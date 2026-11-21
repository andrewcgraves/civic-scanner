import { describe, expect, it } from "vitest";

import {
  FORM_ERROR,
  NEW_SUBSCRIPTION_PATH,
  SUBSCRIPTIONS_PATH,
  SUBSCRIPTIONS_SIGN_IN_PATH,
  agencyName,
  checkUrl,
  checkValues,
  editPath,
  initialForm,
  isSlug,
  isSubscriptionId,
  outcomePath,
  readId,
  readOutcome,
  readValues,
  rejection,
} from "./state";

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
      values: { agency: "octa", url: "" },
    });
    expect(initialForm({ url: "https://a.example" })).toEqual({
      kind: "idle",
      values: { agency: "", url: "https://a.example" },
    });
  });
});

describe("readValues and readId", () => {
  it("reads trimmed agency, id and the URL as typed", () => {
    const data = form({
      agency: " octa ",
      url: " https://a.example ",
      id: ` ${ID} `,
    });

    expect(readValues(data)).toEqual({
      agency: "octa",
      url: " https://a.example ",
    });
    expect(readId(data)).toBe(ID);
  });

  it("reads missing or file fields as empty, and caps long input", () => {
    const data = form({ url: "x".repeat(5000), agency: new Blob(["a"]) });

    expect(readValues(data)).toEqual({ agency: "", url: "x".repeat(4096) });
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

  it("requires an agency slug and a URL", () => {
    expect(checkValues({ agency: "", url: "" })).toEqual({
      agency: "Choose an agency.",
      url: "Enter the URL to send webhooks to.",
    });
    expect(checkValues({ agency: "octa", url: "https://a.example" })).toEqual(
      {},
    );
  });
});

describe("rejection", () => {
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
