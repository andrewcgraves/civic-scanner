import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const nextServer = vi.hoisted(() => ({
  connection: vi.fn(() => Promise.resolve()),
}));

vi.mock("next/server", () => ({ connection: nextServer.connection }));

async function loadServerFlags(env: Record<string, string | undefined>) {
  for (const [name, value] of Object.entries(env)) {
    vi.stubEnv(name, value);
  }
  vi.resetModules();
  return import("./server");
}

function flagsResponse(flags: Record<string, boolean>) {
  return new Response(
    JSON.stringify({
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
      errorsWhileComputingFlags: false,
      requestId: "request-1",
    }),
    { status: 200, headers: { "content-type": "application/json" } },
  );
}

function stubPostHog(answer: (init?: RequestInit) => Promise<Response>) {
  const fetchMock = vi.fn((_url: string, init?: RequestInit) => answer(init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function requestsTo(fetchMock: ReturnType<typeof stubPostHog>) {
  return fetchMock.mock.calls.map(([url, init]) => ({
    url,
    body: JSON.parse(
      typeof init?.body === "string" ? init.body : "{}",
    ) as Record<string, unknown>,
  }));
}

describe("server flags in memory", () => {
  beforeEach(() => {
    nextServer.connection.mockClear();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("turns on the flags listed in NEXT_PUBLIC_FEATURE_FLAGS", async () => {
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search, meeting-alerts",
    });

    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(await isFlagEnabled("meeting-alerts")).toBe(true);
    expect(await getFlagsOn()).toEqual(["new-search", "meeting-alerts"]);
  });

  it("turns off a flag that is not listed", async () => {
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search",
    });

    expect(await isFlagEnabled("new-search-v2")).toBe(false);
  });

  it("turns every flag off when NEXT_PUBLIC_FEATURE_FLAGS is unset", async () => {
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(await isFlagEnabled("new-search")).toBe(false);
    expect(await isFlagEnabled("")).toBe(false);
    expect(await getFlagsOn()).toEqual([]);
  });

  it("turns every flag on when NEXT_PUBLIC_FEATURE_FLAGS is *", async () => {
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: " * ",
    });

    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(await isFlagEnabled("anything-at-all")).toBe(true);
    expect(await getFlagsOn()).toBe("all");
  });

  it("leaves the page free to render statically", async () => {
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "new-search",
    });

    await isFlagEnabled("new-search");
    await getFlagsOn();

    expect(nextServer.connection).not.toHaveBeenCalled();
  });
});

describe("server flags from PostHog", () => {
  beforeEach(() => {
    nextServer.connection.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("asks PostHog once for every flag of the anonymous visitor", async () => {
    const fetchMock = stubPostHog(() =>
      Promise.resolve(
        flagsResponse({ "new-search": true, "meeting-alerts": false }),
      ),
    );
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: " phc_test ",
      NEXT_PUBLIC_POSTHOG_HOST: "https://eu.i.posthog.com",
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(await getFlagsOn()).toEqual(["new-search"]);
    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(await isFlagEnabled("meeting-alerts")).toBe(false);

    const requests = requestsTo(fetchMock);
    expect(requests).toHaveLength(1);
    expect(requests[0]?.url).toMatch(/^https:\/\/eu\.i\.posthog\.com\/flags\//);
    expect(requests[0]?.body).toMatchObject({
      token: "phc_test",
      distinct_id: "anonymous",
    });
    expect(requests[0]?.body).not.toHaveProperty("flag_keys_to_evaluate");
  });

  it("uses the PostHog US host and ignores the in-memory list", async () => {
    const fetchMock = stubPostHog(() =>
      Promise.resolve(flagsResponse({ "new-search": false })),
    );
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
      NEXT_PUBLIC_POSTHOG_HOST: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "*",
    });

    expect(await isFlagEnabled("new-search")).toBe(false);
    expect(requestsTo(fetchMock)[0]?.url).toMatch(
      /^https:\/\/us\.i\.posthog\.com\/flags\//,
    );
  });

  it("renders the page per request, before asking PostHog", async () => {
    const fetchMock = stubPostHog(() =>
      Promise.resolve(flagsResponse({ "new-search": true })),
    );
    const { getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    await getFlagsOn();

    expect(nextServer.connection).toHaveBeenCalled();
    const [connected] = nextServer.connection.mock.invocationCallOrder;
    const [asked] = fetchMock.mock.invocationCallOrder;
    expect(connected).toBeLessThan(asked ?? 0);
  });

  it("asks PostHog at most once every 30 seconds", async () => {
    vi.useFakeTimers();
    const fetchMock = stubPostHog(() =>
      Promise.resolve(flagsResponse({ "new-search": true })),
    );
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    expect(await getFlagsOn()).toEqual(["new-search"]);
    expect(await isFlagEnabled("new-search")).toBe(true);
    expect(await isFlagEnabled("meeting-alerts")).toBe(false);
    vi.advanceTimersByTime(29_999);
    expect(await getFlagsOn()).toEqual(["new-search"]);
    expect(fetchMock).toHaveBeenCalledOnce();

    vi.advanceTimersByTime(1);
    await getFlagsOn();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("sends PostHog nothing but the flag request", async () => {
    vi.useFakeTimers();
    const fetchMock = stubPostHog(() =>
      Promise.resolve(flagsResponse({ "new-search": true })),
    );
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    await isFlagEnabled("new-search");
    for (let second = 0; second < 60; second += 1) {
      await vi.advanceTimersByTimeAsync(1_000);
    }

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(requestsTo(fetchMock)[0]?.url).toContain("/flags/");
  });

  it("turns off a flag PostHog does not know", async () => {
    stubPostHog(() => Promise.resolve(flagsResponse({ "new-search": true })));
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    expect(await isFlagEnabled("new-search-v2")).toBe(false);
  });

  it("turns every flag off and reports it when PostHog fails", async () => {
    vi.useFakeTimers();
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const fetchMock = stubPostHog(() =>
      Promise.resolve(new Response("bad gateway", { status: 502 })),
    );
    const { isFlagEnabled, getFlagsOn } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    const flags = getFlagsOn();
    await vi.advanceTimersByTimeAsync(10_000);

    await expect(flags).resolves.toEqual([]);
    expect(await isFlagEnabled("new-search")).toBe(false);
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(consoleError).toHaveBeenCalledWith(
      "Feature flags could not be evaluated",
      expect.anything(),
    );
  });

  it("turns every flag off and reports it when the PostHog client throws", async () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const failure = new Error("client bug");
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });
    const { PostHog } = await import("posthog-node");
    vi.spyOn(PostHog.prototype, "evaluateFlags").mockRejectedValue(failure);

    expect(await isFlagEnabled("new-search")).toBe(false);
    expect(consoleError).toHaveBeenCalledWith(
      "Feature flags could not be evaluated",
      failure,
    );
  });

  it("turns every flag off when PostHog cannot be reached", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    stubPostHog(() => Promise.reject(new TypeError("fetch failed")));
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    expect(await isFlagEnabled("new-search")).toBe(false);
  });

  it("gives up on an unanswered request after half a second", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const fetchMock = stubPostHog(
      (init) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => {
            reject(new DOMException("aborted", "AbortError"));
          });
        }),
    );
    const { isFlagEnabled } = await loadServerFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
    });

    const check = { settled: false, result: isFlagEnabled("new-search") };
    void check.result.finally(() => {
      check.settled = true;
    });
    await vi.advanceTimersByTimeAsync(400);
    expect(check.settled).toBe(false);
    await vi.advanceTimersByTimeAsync(200);

    expect(check.settled).toBe(true);
    await expect(check.result).resolves.toBe(false);
    expect(fetchMock).toHaveBeenCalledOnce();
  });
});
