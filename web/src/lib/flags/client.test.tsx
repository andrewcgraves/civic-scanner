import { render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const posthog = vi.hoisted(() => {
  let flagsLoaded: (() => void) | undefined;
  return {
    init: vi.fn(),
    getFeatureFlagResult: vi.fn<() => unknown>(),
    onFeatureFlags: vi.fn((callback: () => void) => {
      flagsLoaded = callback;
      return () => {
        flagsLoaded = undefined;
      };
    }),
    reloadFeatureFlags: vi.fn(() => {
      flagsLoaded?.();
    }),
  };
});

vi.mock("posthog-js", () => ({ default: posthog }));

async function renderFlag(
  flag: string,
  env: Record<string, string | undefined>,
) {
  for (const [name, value] of Object.entries(env)) {
    vi.stubEnv(name, value);
  }
  vi.resetModules();
  const { FlagsProvider, useFlag } = await import("./client");

  function Gated() {
    return <p>{useFlag(flag) ? "on" : "off"}</p>;
  }

  render(
    <FlagsProvider>
      <Gated />
    </FlagsProvider>,
  );
}

describe("useFlag with in-memory flags", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("is on for a listed flag", async () => {
    await renderFlag("new-search", {
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "meeting-alerts,new-search",
    });

    expect(screen.getByText("on")).toBeInTheDocument();
  });

  it("is off for a flag that is not listed", async () => {
    await renderFlag("new-search-v2", {
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search",
    });

    expect(screen.getByText("off")).toBeInTheDocument();
  });

  it("is on for any flag when every flag is on", async () => {
    await renderFlag("anything-at-all", {
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "*",
    });

    expect(screen.getByText("on")).toBeInTheDocument();
  });
});

describe("useFlag with PostHog", () => {
  beforeEach(() => {
    posthog.init.mockClear();
    posthog.getFeatureFlagResult.mockReset();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("follows PostHog once its flags load", async () => {
    posthog.getFeatureFlagResult.mockReturnValue({
      key: "new-search",
      enabled: true,
    });

    await renderFlag("new-search", {
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
      NEXT_PUBLIC_POSTHOG_HOST: "https://eu.i.posthog.com",
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(await screen.findByText("on")).toBeInTheDocument();
    expect(posthog.getFeatureFlagResult).toHaveBeenCalledWith("new-search", {
      send_event: false,
    });
  });

  it("starts PostHog for flags only, without analytics", async () => {
    await renderFlag("new-search", { NEXT_PUBLIC_POSTHOG_KEY: "phc_test" });

    expect(posthog.init).toHaveBeenCalledWith(
      "phc_test",
      expect.objectContaining({
        api_host: "https://us.i.posthog.com",
        autocapture: false,
        capture_pageview: false,
        capture_pageleave: false,
        capture_performance: false,
        disable_session_recording: true,
      }),
    );
  });

  it("is off for a flag PostHog does not know", async () => {
    posthog.getFeatureFlagResult.mockReturnValue(undefined);

    await renderFlag("new-search", { NEXT_PUBLIC_POSTHOG_KEY: "phc_test" });

    await vi.waitFor(() => {
      expect(posthog.getFeatureFlagResult).toHaveBeenCalled();
    });
    expect(screen.getByText("off")).toBeInTheDocument();
  });

  it("is off when PostHog fails", async () => {
    posthog.getFeatureFlagResult.mockImplementation(() => {
      throw new Error("flags unavailable");
    });

    await renderFlag("new-search", { NEXT_PUBLIC_POSTHOG_KEY: "phc_test" });

    await vi.waitFor(() => {
      expect(posthog.getFeatureFlagResult).toHaveBeenCalled();
    });
    expect(screen.getByText("off")).toBeInTheDocument();
  });

  it("renders flags off on the server without starting PostHog", async () => {
    posthog.getFeatureFlagResult.mockReturnValue({
      key: "new-search",
      enabled: true,
    });
    vi.stubGlobal("window", undefined);
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", "phc_test");
    vi.resetModules();
    const { FlagsProvider, useFlag } = await import("./client");

    function Gated() {
      return <p>{useFlag("new-search") ? "on" : "off"}</p>;
    }

    const html = renderToStaticMarkup(
      <FlagsProvider>
        <Gated />
      </FlagsProvider>,
    );
    vi.unstubAllGlobals();

    expect(html).toBe("<p>off</p>");
    expect(posthog.init).not.toHaveBeenCalled();
  });
});
