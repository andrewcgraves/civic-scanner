import { afterEach, describe, expect, it, vi } from "vitest";

import { sessionCookieName } from "./cookie";

describe("sessionCookieName", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("is AuthKit's default without WORKOS_COOKIE_NAME", () => {
    vi.stubEnv("WORKOS_COOKIE_NAME", undefined);

    expect(sessionCookieName()).toBe("wos-session");
  });

  it("is AuthKit's default when WORKOS_COOKIE_NAME is blank", () => {
    vi.stubEnv("WORKOS_COOKIE_NAME", " ");

    expect(sessionCookieName()).toBe("wos-session");
  });

  it("follows WORKOS_COOKIE_NAME", () => {
    vi.stubEnv("WORKOS_COOKIE_NAME", "civic-session");

    expect(sessionCookieName()).toBe("civic-session");
  });
});
