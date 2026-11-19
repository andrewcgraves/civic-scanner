import { describe, expect, it } from "vitest";

import { parseSessionState } from "./session-state";

describe("parseSessionState", () => {
  it("reads a signed-out state", () => {
    expect(parseSessionState({ signedIn: false })).toEqual({
      signedIn: false,
    });
  });

  it("reads a signed-in state with an email", () => {
    expect(
      parseSessionState({ signedIn: true, email: "a@example.com" }),
    ).toEqual({ signedIn: true, email: "a@example.com" });
  });

  it("reads a signed-in state without an email", () => {
    expect(parseSessionState({ signedIn: true, email: 42 })).toEqual({
      signedIn: true,
      email: null,
    });
  });

  it("keeps only the known fields", () => {
    expect(
      parseSessionState({ signedIn: true, email: null, accessToken: "t" }),
    ).toEqual({ signedIn: true, email: null });
  });

  it.each([[null], ["signed-in"], [{}], [{ signedIn: "true" }]])(
    "refuses %j",
    (value) => {
      expect(parseSessionState(value)).toBeNull();
    },
  );
});
