import { describe, expect, it } from "vitest";

import { parseSessionState } from "./session-state";

describe("parseSessionState", () => {
  it("reads a signed-out state", () => {
    expect(parseSessionState({ signedIn: false })).toEqual({
      signedIn: false,
    });
  });

  it("reads a signed-in state with an email and a role", () => {
    expect(
      parseSessionState({
        signedIn: true,
        email: "a@example.com",
        role: "admin",
      }),
    ).toEqual({ signedIn: true, email: "a@example.com", role: "admin" });
  });

  it("reads a signed-in state without an email or a role", () => {
    expect(parseSessionState({ signedIn: true, email: 42 })).toEqual({
      signedIn: true,
      email: null,
      role: null,
    });
  });

  it.each([["superuser"], ["Admin"], [""], [true], [null]])(
    "treats the role %j as unknown",
    (role) => {
      expect(parseSessionState({ signedIn: true, email: null, role })).toEqual({
        signedIn: true,
        email: null,
        role: null,
      });
    },
  );

  it("keeps only the known fields", () => {
    expect(
      parseSessionState({
        signedIn: true,
        email: null,
        role: "user",
        accessToken: "t",
      }),
    ).toEqual({ signedIn: true, email: null, role: "user" });
  });

  it.each([[null], ["signed-in"], [{}], [{ signedIn: "true" }]])(
    "refuses %j",
    (value) => {
      expect(parseSessionState(value)).toBeNull();
    },
  );
});
