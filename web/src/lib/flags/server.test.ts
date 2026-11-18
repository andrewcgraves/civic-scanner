import {
  afterEach,
  beforeEach,
  describe,
  expect,
  expectTypeOf,
  it,
  vi,
} from "vitest";

import type { FlagKey } from "./registry";

const nextServer = vi.hoisted(() => ({
  connection: vi.fn(() => Promise.resolve()),
}));

vi.mock("next/server", () => ({ connection: nextServer.connection }));

vi.mock("./registry", () => ({
  FLAG_KEYS: ["new-search", "meeting-alerts"],
}));

const NEW_SEARCH = "new-search" as FlagKey;
const MEETING_ALERTS = "meeting-alerts" as FlagKey;
const UNREGISTERED = "not-registered" as FlagKey;

async function loadServerFlags(env: Record<string, string | undefined>) {
  for (const [name, value] of Object.entries(env)) {
    vi.stubEnv(name, value);
  }
  vi.resetModules();
  return import("./server");
}

function flagsResponse(flags: Record<string, boolean>) {
  return Response.json({
    flags: Object.fromEntries(
      Object.entries(flags).map(([key, enabled]) => [
        key,
        {
          key,
          enabled,
          variant: null,
          reason: { code: "condition_match", description: "matched" },
          metadata: { id: 1, version: 1, payload: null },
        },
      ]),
    ),
  });
}

function stubPostHog(flags: Record<string, boolean>) {
  const fetchMock = vi.fn<
    (url: string, init?: RequestInit) => Promise<Response>
  >(() => Promise.resolve(flagsResponse(flags)));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(async () => {
  const { OpenFeature } = await import("@openfeature/server-sdk");
  await OpenFeature.clearProviders();
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("server flags", () => {
  it("accept only registered flag keys", async () => {
    const { isFlagEnabled } = await loadServerFlags({});

    expectTypeOf(isFlagEnabled).parameter(0).toEqualTypeOf<FlagKey>();
  });
});

describe("server flags in memory", () => {
  beforeEach(() => {
    nextServer.connection.mockClear();
  });

  it("turns on the registered flags listed in NEXT_PUBLIC_FEATURE_FLAGS", async () => {
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search, not-registered",
    });

    expect(await getFlagsOn()).toEqual(["new-search"]);
    expect(await isFlagEnabled(NEW_SEARCH)).toBe(true);
    expect(await isFlagEnabled(MEETING_ALERTS)).toBe(false);
    expect(await isFlagEnabled(UNREGISTERED)).toBe(false);
  });

  it("turns every flag off when NEXT_PUBLIC_FEATURE_FLAGS is unset", async () => {
    const { getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(await getFlagsOn()).toEqual([]);
  });

  it("turns every registered flag on when NEXT_PUBLIC_FEATURE_FLAGS is *", async () => {
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: " * ",
    });

    expect(await getFlagsOn()).toEqual(["new-search", "meeting-alerts"]);
    expect(await isFlagEnabled(UNREGISTERED)).toBe(false);
  });

  it("leaves the page free to render statically", async () => {
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search",
    });

    await getFlagsOn();
    await isFlagEnabled(NEW_SEARCH);

    expect(nextServer.connection).not.toHaveBeenCalled();
  });

  it("turns flags off and reports it when the provider fails to start", async () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const failure = new Error("provider broke");
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "*",
    });
    const { OpenFeature } = await import("@openfeature/server-sdk");
    vi.spyOn(OpenFeature, "setProviderAndWait").mockRejectedValue(failure);

    expect(await isFlagEnabled(NEW_SEARCH)).toBe(false);
    expect(consoleError).toHaveBeenCalledWith(
      "Feature flag provider failed to start",
      failure,
    );
  });
});

describe("server flags from PostHog", () => {
  beforeEach(() => {
    nextServer.connection.mockClear();
  });

  it("asks PostHog once for every registered flag of the anonymous visitor", async () => {
    const fetchMock = stubPostHog({
      "new-search": true,
      "meeting-alerts": false,
      "not-registered": true,
    });
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: " phc_test ",
      NEXT_PUBLIC_POSTHOG_HOST: "https://eu.i.posthog.com",
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(await getFlagsOn()).toEqual(["new-search"]);
    expect(await isFlagEnabled(NEW_SEARCH)).toBe(true);
    expect(await isFlagEnabled(MEETING_ALERTS)).toBe(false);
    expect(await isFlagEnabled(UNREGISTERED)).toBe(false);

    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toMatch(/^https:\/\/eu\.i\.posthog\.com\/flags\//);
    const body = typeof init?.body === "string" ? init.body : "{}";
    expect(JSON.parse(body)).toMatchObject({
      token: "phc_test",
      distinct_id: "anonymous",
    });
  });

  it("uses the PostHog US host and ignores the in-memory list", async () => {
    const fetchMock = stubPostHog({ "new-search": false });
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
      NEXT_PUBLIC_POSTHOG_HOST: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "*",
    });

    expect(await isFlagEnabled(NEW_SEARCH)).toBe(false);
    expect(fetchMock.mock.calls[0]?.[0]).toMatch(
      /^https:\/\/us\.i\.posthog\.com\/flags\//,
    );
  });

  it("renders the page per request, before asking PostHog", async () => {
    const fetchMock = stubPostHog({ "new-search": true });
    const { getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    await getFlagsOn();

    expect(nextServer.connection).toHaveBeenCalled();
    const [connected] = nextServer.connection.mock.invocationCallOrder;
    const [asked] = fetchMock.mock.invocationCallOrder;
    expect(connected).toBeLessThan(asked ?? 0);
  });
});
