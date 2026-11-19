import { describe, expect, it } from "vitest";

import { safeReturnPath } from "./return-path";

describe("safeReturnPath", () => {
  it.each([
    ["/", "/"],
    ["/agencies", "/agencies"],
    ["/agencies/la-metro?tab=meetings", "/agencies/la-metro?tab=meetings"],
    ["/agencies#top", "/agencies"],
    ["/a/../agencies", "/agencies"],
    ["/caf%C3%A9", "/caf%C3%A9"],
  ])("keeps the same-origin path %s", (value, expected) => {
    expect(safeReturnPath(value)).toBe(expected);
  });

  it.each([
    [null],
    [undefined],
    [""],
    ["agencies"],
    ["https://evil.example/"],
    ["http://evil.example"],
    ["//evil.example"],
    ["//evil.example/path"],
    ["///evil.example"],
    ["/\\evil.example"],
    ["\\\\evil.example"],
    ["/\tevil"],
    ["/\nevil"],
    ["/\u0000"],
    ["/\u007f"],
    ["javascript:alert(1)"],
    ["data:text/html,hi"],
    [" /agencies"],
    ["/auth/sign-in"],
    ["/auth/callback?code=x"],
    ["/auth"],
    ["/%2e%2e/auth/callback"],
    ["/x/%2E%2E/auth/session"],
    [`/${"a".repeat(2048)}`],
  ])("falls back to / for %j", (value) => {
    expect(safeReturnPath(value)).toBe("/");
  });

  it("keeps paths that only start like the auth routes", () => {
    expect(safeReturnPath("/authors")).toBe("/authors");
  });
});
