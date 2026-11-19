import { describe, expect, it } from "vitest";

import { safeReturnPath } from "./return-path";

const ORIGIN = "https://civic.example";

function landsOnSite(path: string): boolean {
  const target = new URL(ORIGIN);
  const parsed = new URL(path, "https://placeholder.invalid");
  target.pathname = parsed.pathname;
  target.search = parsed.search;
  return new URL(target.toString()).origin === ORIGIN;
}

describe("safeReturnPath", () => {
  it.each([
    ["/", "/"],
    ["/agencies", "/agencies"],
    ["/agencies/la-metro?tab=meetings", "/agencies/la-metro?tab=meetings"],
    ["/ok?next=//evil.example#frag", "/ok?next=//evil.example"],
    ["/agencies#top", "/agencies"],
    ["/ok#//evil", "/ok"],
    ["/a/../agencies", "/agencies"],
    ["/auth/%2e%2e/x", "/x"],
    ["/caf%C3%A9", "/caf%C3%A9"],
    ["/authors", "/authors"],
    ["/@evil.example", "/@evil.example"],
    ["/javascript:alert(1)", "/javascript:alert(1)"],
    ["/∕∕evil.example", "/%E2%88%95%E2%88%95evil.example"],
    ["/／／evil.example", "/%EF%BC%8F%EF%BC%8Fevil.example"],
    ["/​/evil.example", "/%E2%80%8B/evil.example"],
  ])("keeps the same-origin path %j as %j", (value, expected) => {
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
    ["/.//evil.example"],
    ["/x/..//evil.example"],
    ["/x/../..//evil.example"],
    ["/./\\evil.example"],
    ["/%2F%2Fevil.example"],
    ["/%2f/evil.example"],
    ["/.%2F%2Fevil.example"],
    ["/\\evil.example"],
    ["/%5Cevil.example"],
    ["/%5cevil.example"],
    ["\\\\evil.example"],
    ["/\tevil"],
    ["/\t/evil.example"],
    ["/\n/evil.example"],
    ["/\u0000evil"],
    ["/\u007f"],
    ["/ok "],
    ["javascript:alert(1)"],
    ["data:text/html,hi"],
    [" /agencies"],
    ["/auth"],
    ["/auth/sign-in"],
    ["/auth/callback?code=x"],
    ["/AUTH/callback"],
    ["/auth%2fcallback"],
    ["/%61uth/callback"],
    ["/auth/..%2fsession"],
    ["/%2e%2e/auth/callback"],
    ["/x/%2E%2E/auth/session"],
    ["/x/..%2f..%2fauth/callback"],
    ["/%E0%A4%A"],
    [`/${"a".repeat(2048)}`],
  ])("falls back to / for %j", (value) => {
    expect(safeReturnPath(value)).toBe("/");
  });

  it.each([
    "/.//evil.example",
    "/x/..//evil.example",
    "/%2F%2Fevil.example",
    "/∕∕evil.example",
    "/／／evil.example",
    "/。evil",
    "/＼evil.example",
    "/auth;x/callback",
    "/ok?next=//evil.example",
  ])("never yields a path that leaves the site: %j", (value) => {
    const path = safeReturnPath(value);

    expect(path.startsWith("//")).toBe(false);
    expect(landsOnSite(path)).toBe(true);
  });

  it("is idempotent", () => {
    for (const value of ["/a/../b?c=d", "/caf%C3%A9", "/x/./y"]) {
      const once = safeReturnPath(value);
      expect(safeReturnPath(once)).toBe(once);
    }
  });
});
