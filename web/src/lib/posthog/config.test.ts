import { afterEach, describe, expect, it, vi } from "vitest";

import { readPostHogProject } from "./config";

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("readPostHogProject", () => {
  it("is undefined without a key", () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", " ");

    expect(readPostHogProject()).toBeUndefined();
  });

  it("defaults to the PostHog US host", () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", " phc_test ");
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", "");

    expect(readPostHogProject()).toEqual({
      key: "phc_test",
      host: "https://us.i.posthog.com",
    });
  });

  it("uses the configured host", () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", "phc_test");
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", " https://ph.civic.example ");

    expect(readPostHogProject()).toEqual({
      key: "phc_test",
      host: "https://ph.civic.example",
    });
  });
});
