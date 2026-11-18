import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const posthog = vi.hoisted(() => ({
  created: vi.fn<(key: string, options: unknown) => void>(),
  getFeatureFlagResult: vi.fn<() => Promise<unknown>>(),
  reloadFeatureFlags: vi.fn<() => Promise<void>>(),
}));

const nextServer = vi.hoisted(() => ({
  connection: vi.fn(() => Promise.resolve()),
}));

vi.mock("next/server", () => ({ connection: nextServer.connection }));

vi.mock("posthog-node", () => ({
  PostHog: class {
    constructor(key: string, options: unknown) {
      posthog.created(key, options);
    }
    getFeatureFlagResult = posthog.getFeatureFlagResult;
    reloadFeatureFlags = posthog.reloadFeatureFlags;
    on = () => () => undefined;
  },
}));

async function loadServerFlags(env: Record<string, string | undefined>) {
  for (const [name, value] of Object.entries(env)) {
    vi.stubEnv(name, value);
  }
  vi.resetModules();
  return import("./server");
}

describe("isFlagEnabled with in-memory flags", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("turns on the flags listed in NEXT_PUBLIC_FEATURE_FLAGS", async () => {
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search, meeting-alerts",
    });

    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(await isFlagEnabled("meeting-alerts")).toBe(true);
  });

  it("turns off a flag that is not listed", async () => {
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search",
    });

    expect(await isFlagEnabled("new-search-v2")).toBe(false);
  });

  it("turns every flag off when NEXT_PUBLIC_FEATURE_FLAGS is unset", async () => {
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(await isFlagEnabled("new-search")).toBe(false);
    expect(await isFlagEnabled("")).toBe(false);
  });

  it("turns every flag on when NEXT_PUBLIC_FEATURE_FLAGS is *", async () => {
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: " * ",
    });

    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(await isFlagEnabled("anything-at-all")).toBe(true);
  });

  it("leaves the page free to render statically", async () => {
    nextServer.connection.mockClear();
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search",
    });

    await isFlagEnabled("new-search");

    expect(nextServer.connection).not.toHaveBeenCalled();
  });
});

describe("isFlagEnabled with PostHog", () => {
  beforeEach(() => {
    nextServer.connection.mockClear();
    posthog.created.mockReset();
    posthog.getFeatureFlagResult.mockReset();
    posthog.reloadFeatureFlags.mockReset().mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllEnvs();
  });

  it("asks PostHog when a PostHog key is configured", async () => {
    posthog.getFeatureFlagResult.mockResolvedValue({
      key: "new-search",
      enabled: true,
    });
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: " phc_test ",
      NEXT_PUBLIC_POSTHOG_HOST: "https://eu.i.posthog.com",
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(posthog.created).toHaveBeenCalledWith("phc_test", {
      host: "https://eu.i.posthog.com",
      featureFlagsRequestTimeoutMs: 500,
      featureFlagsRequestMaxRetries: 0,
    });
    expect(posthog.getFeatureFlagResult).toHaveBeenCalledWith(
      "new-search",
      expect.any(String),
      expect.objectContaining({ sendFeatureFlagEvents: false }),
    );
  });

  it("renders the page per request, before asking PostHog", async () => {
    posthog.getFeatureFlagResult.mockResolvedValue({
      key: "new-search",
      enabled: true,
    });
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    await isFlagEnabled("new-search");

    expect(nextServer.connection).toHaveBeenCalledOnce();
    const [connected] = nextServer.connection.mock.invocationCallOrder;
    const [asked] = posthog.getFeatureFlagResult.mock.invocationCallOrder;
    expect(connected).toBeLessThan(asked ?? 0);
  });

  it("asks PostHog about each flag at most once every 30 seconds", async () => {
    vi.useFakeTimers();
    posthog.getFeatureFlagResult.mockResolvedValue({
      key: "new-search",
      enabled: true,
    });
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(await isFlagEnabled("new-search")).toBe(true);
    vi.advanceTimersByTime(29_999);
    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(posthog.getFeatureFlagResult).toHaveBeenCalledTimes(1);

    await isFlagEnabled("meeting-alerts");
    expect(posthog.getFeatureFlagResult).toHaveBeenCalledTimes(2);

    vi.advanceTimersByTime(1);
    await isFlagEnabled("new-search");
    expect(posthog.getFeatureFlagResult).toHaveBeenCalledTimes(3);
  });

  it("uses the PostHog US host and ignores the in-memory list", async () => {
    posthog.getFeatureFlagResult.mockResolvedValue({
      key: "new-search",
      enabled: false,
    });
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
      NEXT_PUBLIC_POSTHOG_HOST: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "*",
    });

    expect(await isFlagEnabled("new-search")).toBe(false);
    expect(posthog.created).toHaveBeenCalledWith("phc_test", {
      host: "https://us.i.posthog.com",
      featureFlagsRequestTimeoutMs: 500,
      featureFlagsRequestMaxRetries: 0,
    });
  });

  it("turns off a flag PostHog does not know", async () => {
    posthog.getFeatureFlagResult.mockResolvedValue(undefined);
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    expect(await isFlagEnabled("new-search")).toBe(false);
  });

  it("turns a flag off when PostHog fails", async () => {
    posthog.getFeatureFlagResult.mockRejectedValue(new Error("unreachable"));
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    expect(await isFlagEnabled("new-search")).toBe(false);
  });

  it("turns flags off and reports it when PostHog fails to start", async () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const failure = new Error("unreachable");
    posthog.reloadFeatureFlags.mockRejectedValue(failure);
    posthog.getFeatureFlagResult.mockRejectedValue(failure);
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    await expect(isFlagEnabled("new-search")).resolves.toBe(false);
    await expect(isFlagEnabled("new-search")).resolves.toBe(false);
    expect(consoleError).toHaveBeenCalledWith(
      "Feature flag provider failed to start",
      failure,
    );
    consoleError.mockRestore();
  });
});
