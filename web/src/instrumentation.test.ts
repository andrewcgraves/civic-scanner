import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ getAuthConfig: vi.fn() }));

vi.mock("@/lib/auth/config", () => ({ getAuthConfig: mocks.getAuthConfig }));

import { register } from "./instrumentation";

describe("register", () => {
  it("checks the WorkOS configuration at startup", () => {
    register();

    expect(mocks.getAuthConfig).toHaveBeenCalledTimes(1);
  });
});
