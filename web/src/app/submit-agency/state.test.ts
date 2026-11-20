import { describe, expect, it } from "vitest";

import {
  SIGN_IN_PATH,
  checkValues,
  fieldErrorsFrom,
  readValues,
} from "./state";

function form(entries: Record<string, string | Blob>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(entries)) {
    data.append(key, value);
  }
  return data;
}

describe("SIGN_IN_PATH", () => {
  it("returns to the page after sign-in", () => {
    expect(SIGN_IN_PATH).toBe("/auth/sign-in?returnTo=%2Fsubmit-agency");
  });
});

describe("readValues", () => {
  it("reads both fields as strings", () => {
    expect(
      readValues(form({ name: "LA Metro", website: "metro.net" })),
    ).toEqual({ name: "LA Metro", website: "metro.net" });
  });

  it("treats missing fields and files as empty", () => {
    expect(readValues(form({ website: new Blob(["x"]) }))).toEqual({
      name: "",
      website: "",
    });
  });

  it("keeps at most 4096 characters of each field", () => {
    const values = readValues(form({ name: "a".repeat(5000), website: "b" }));
    expect(values.name).toHaveLength(4096);
  });
});

describe("checkValues", () => {
  it("accepts filled-in fields", () => {
    expect(checkValues({ name: " LA Metro ", website: "metro.net" })).toEqual(
      {},
    );
  });

  it("requires both fields", () => {
    expect(checkValues({ name: "  ", website: "\n" })).toEqual({
      name: "Enter the agency's name.",
      website: "Enter the agency's website.",
    });
  });

  it("counts characters, not UTF-16 units, after collapsing spaces", () => {
    expect(checkValues({ name: "🚆".repeat(200), website: "x.org" })).toEqual(
      {},
    );
    expect(
      checkValues({ name: `${"a ".repeat(100)}   b`, website: "x.org" }),
    ).toEqual({ name: "Name must be at most 200 characters." });
  });

  it("limits the website to 2,048 bytes", () => {
    expect(
      checkValues({ name: "Agency", website: `x.org/${"é".repeat(1022)}` }),
    ).toEqual({ website: "Website must be at most 2,048 characters." });
  });
});

describe("fieldErrorsFrom", () => {
  it("maps each known field to its first message", () => {
    expect(
      fieldErrorsFrom({
        code: "invalid_submission",
        fields: [
          { field: "name", code: "too_short", message: "Too short." },
          { field: "name", code: "other", message: "Second." },
          { field: "website", code: "ip_address", message: "No IPs." },
        ],
      }),
    ).toEqual({ name: "Too short.", website: "No IPs." });
  });

  it("caps long messages", () => {
    const errors = fieldErrorsFrom({
      fields: [{ field: "name", message: "m".repeat(1000) }],
    });
    expect(errors.name).toHaveLength(300);
  });

  it.each([
    ["null", null],
    ["a string", "nope"],
    ["no fields", { code: "invalid_body" }],
    ["fields that are not a list", { fields: "x" }],
    [
      "malformed entries",
      {
        fields: [
          null,
          "x",
          { field: "other", message: "x" },
          { field: "name", message: 3 },
          { field: "website", message: "" },
        ],
      },
    ],
  ])("ignores %s", (_name, body) => {
    expect(fieldErrorsFrom(body)).toEqual({});
  });
});
