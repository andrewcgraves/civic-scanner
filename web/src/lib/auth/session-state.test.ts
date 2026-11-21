import { describe, expect, it } from "vitest";

import { parseSessionState } from "./session-state";

describe("parseSessionState", () => {
  it("reads a signed-out state", () => {
    expect(parseSessionState({ signedIn: false })).toEqual({
      signedIn: false,
    });
  });

  it("reads a signed-in state with an email, a role and a plan", () => {
    expect(
      parseSessionState({
        signedIn: true,
        email: "a@example.com",
        role: "admin",
        plan: "paid",
      }),
    ).toEqual({
      signedIn: true,
      email: "a@example.com",
      role: "admin",
      plan: "paid",
    });
  });

  it("reads the free plan", () => {
    expect(
      parseSessionState({ signedIn: true, email: null, plan: "free" }),
    ).toEqual({ signedIn: true, email: null, role: null, plan: "free" });
  });

  it("reads a signed-in state without an email, a role or a plan", () => {
    expect(parseSessionState({ signedIn: true, email: 42 })).toEqual({
      signedIn: true,
      email: null,
      role: null,
      plan: null,
    });
  });

  it.each([["superuser"], ["Admin"], [""], [true], [null]])(
    "treats the role %j as unknown",
    (role) => {
      expect(parseSessionState({ signedIn: true, email: null, role })).toEqual({
        signedIn: true,
        email: null,
        role: null,
        plan: null,
      });
    },
  );

  it.each([["premium"], ["Paid"], [""], [true], [null]])(
    "treats the plan %j as unknown",
    (plan) => {
      expect(parseSessionState({ signedIn: true, email: null, plan })).toEqual({
        signedIn: true,
        email: null,
        role: null,
        plan: null,
      });
    },
  );

  it("keeps only the known fields", () => {
    expect(
      parseSessionState({
        signedIn: true,
        email: null,
        role: "user",
        plan: "free",
        accessToken: "t",
      }),
    ).toEqual({ signedIn: true, email: null, role: "user", plan: "free" });
  });

  it.each([[null], ["signed-in"], [{}], [{ signedIn: "true" }]])(
    "refuses %j",
    (value) => {
      expect(parseSessionState(value)).toBeNull();
    },
  );
});
