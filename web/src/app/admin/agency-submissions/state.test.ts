import { describe, expect, it } from "vitest";

import {
  REJECTED_PATH,
  REVIEW_SIGN_IN_PATH,
  approvedPath,
  errorCode,
  errorMessage,
  isLiveSlug,
  isSubmissionId,
  readOutcome,
  readReview,
  suggestSlug,
} from "./state";

describe("isLiveSlug", () => {
  it.each(["la-metro", "octa", "a1", "big-bear-transit", "a".repeat(60)])(
    "accepts %s",
    (slug) => {
      expect(isLiveSlug(slug)).toBe(true);
    },
  );

  it.each([
    "",
    "a",
    "a".repeat(61),
    "La-Metro",
    "la_metro",
    "-metro",
    "metro-",
    "la--metro",
    "submitted-3f2a",
    "métro",
    "feed.xml",
  ])("refuses %j", (slug) => {
    expect(isLiveSlug(slug)).toBe(false);
  });
});

describe("suggestSlug", () => {
  it.each([
    ["Big Bear Transit", "big-bear-transit"],
    [
      "  Orange County Transportation Authority ",
      "orange-county-transportation-authority",
    ],
    ["Ventura & Ojai Transit", "ventura-and-ojai-transit"],
    ["Área Metropolitana de São Paulo", "area-metropolitana-de-sao-paulo"],
    ["Metro (L.A.)", "metro-l-a"],
  ])("turns %j into %s", (name, slug) => {
    expect(suggestSlug(name)).toBe(slug);
  });

  it("keeps a long name within the limit without a trailing hyphen", () => {
    const slug = suggestSlug(`${"a".repeat(59)} b`);
    expect(slug).toBe("a".repeat(59));
    expect(isLiveSlug(slug)).toBe(true);
  });

  it.each(["東京都交通局", "!!", "x", "Submitted Agency"])(
    "suggests nothing for %j",
    (name) => {
      expect(suggestSlug(name)).toBe("");
    },
  );
});

describe("isSubmissionId", () => {
  it("accepts a lower-case UUID only", () => {
    expect(isSubmissionId("8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10")).toBe(true);
    expect(isSubmissionId("8B0E6C43-3F3B-4F39-9A31-2A1F7C1A9E10")).toBe(false);
    expect(isSubmissionId("../me")).toBe(false);
    expect(isSubmissionId("")).toBe(false);
  });
});

describe("readReview", () => {
  it("reads and trims the id and slug", () => {
    const data = new FormData();
    data.append("id", " abc ");
    data.append("slug", " big-bear ");
    expect(readReview(data)).toEqual({ id: "abc", slug: "big-bear" });
  });

  it("treats missing or file fields as empty and caps the slug", () => {
    const data = new FormData();
    data.append("id", new Blob(["x"]));
    data.append("slug", "a".repeat(500));
    expect(readReview(data)).toEqual({ id: "", slug: "a".repeat(200) });
    expect(readReview(new FormData())).toEqual({ id: "", slug: "" });
  });
});

describe("error bodies", () => {
  it("reads the code and a capped message", () => {
    const body = { code: "slug_taken", message: "m".repeat(400) };
    expect(errorCode(body)).toBe("slug_taken");
    expect(errorMessage(body)).toBe("m".repeat(300));
  });

  it.each([null, "x", 1, {}, { code: 1, message: "" }])(
    "reads nothing from %j",
    (body) => {
      expect(errorCode(body)).toBeNull();
      expect(errorMessage(body)).toBeNull();
    },
  );
});

describe("paths and outcomes", () => {
  it("builds the paths", () => {
    expect(REVIEW_SIGN_IN_PATH).toBe(
      "/auth/sign-in?returnTo=%2Fadmin%2Fagency-submissions",
    );
    expect(approvedPath("big-bear")).toBe(
      "/admin/agency-submissions?approved=big-bear",
    );
    expect(REJECTED_PATH).toBe("/admin/agency-submissions?rejected=1");
  });

  it("reads an approval or a rejection", () => {
    expect(readOutcome({ approved: "big-bear" })).toEqual({
      kind: "approved",
      slug: "big-bear",
    });
    expect(readOutcome({ rejected: "1" })).toEqual({ kind: "rejected" });
  });

  it.each([
    {},
    { approved: "<script>" },
    { approved: ["a-b", "c-d"] },
    { rejected: "yes" },
    { rejected: ["1"] },
  ])("ignores %j", (params) => {
    expect(readOutcome(params)).toBeNull();
  });
});
