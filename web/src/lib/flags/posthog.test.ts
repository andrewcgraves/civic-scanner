import { OpenFeature } from "@openfeature/server-sdk";
import { PostHog } from "posthog-node";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PostHogProvider } from "./posthog";

type Answer = boolean | string;

function flagsResponse(flags: Record<string, Answer>) {
  return Response.json({
    flags: Object.fromEntries(
      Object.entries(flags).map(([key, value]) => [
        key,
        {
          key,
          enabled: value !== false,
          variant: typeof value === "string" ? value : null,
          reason: { code: "condition_match", description: "matched" },
          metadata: { id: 1, version: 1, payload: null },
        },
      ]),
    ),
    errorsWhileComputingFlags: false,
    requestId: "request-1",
  });
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

async function flagsClient() {
  await OpenFeature.setProviderAndWait(
    "posthog-test",
    new PostHogProvider({
      key: "phc_test",
      host: "https://eu.i.posthog.com",
      distinctId: "anonymous",
    }),
  );
  return OpenFeature.getClient("posthog-test");
}

describe("PostHogProvider", () => {
  afterEach(async () => {
    await OpenFeature.clearProviders();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("answers every flag from one PostHog request for the identity", async () => {
    const fetchMock = stubPostHog(() =>
      Promise.resolve(
        flagsResponse({ "new-search": true, "meeting-alerts": false }),
      ),
    );
    const client = await flagsClient();

    const answers = await Promise.all([
      client.getBooleanValue("new-search", false),
      client.getBooleanValue("meeting-alerts", false),
    ]);

    expect(answers).toEqual([true, false]);
    const requests = requestsTo(fetchMock);
    expect(requests).toHaveLength(1);
    expect(requests[0]?.url).toMatch(/^https:\/\/eu\.i\.posthog\.com\/flags\//);
    expect(requests[0]?.body).toMatchObject({
      token: "phc_test",
      distinct_id: "anonymous",
    });
    expect(requests[0]?.body).not.toHaveProperty("flag_keys_to_evaluate");
  });

  it("asks PostHog at most once every 30 seconds", async () => {
    vi.useFakeTimers();
    const fetchMock = stubPostHog(() =>
      Promise.resolve(flagsResponse({ "new-search": true })),
    );
    const client = await flagsClient();

    expect(await client.getBooleanValue("new-search", false)).toBe(true);
    vi.advanceTimersByTime(29_999);
    expect(await client.getBooleanValue("new-search", false)).toBe(true);
    expect(await client.getBooleanValue("meeting-alerts", false)).toBe(false);
    expect(fetchMock).toHaveBeenCalledOnce();

    vi.advanceTimersByTime(1);
    await client.getBooleanValue("new-search", false);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("turns off a flag PostHog does not know", async () => {
    stubPostHog(() => Promise.resolve(flagsResponse({ "new-search": true })));
    const client = await flagsClient();

    const details = await client.getBooleanDetails("new-search-v2", false);

    expect(details.value).toBe(false);
    expect(details.errorCode).toBe("FLAG_NOT_FOUND");
  });

  it("turns off a multivariate flag", async () => {
    stubPostHog(() =>
      Promise.resolve(flagsResponse({ "new-search": "variant-b" })),
    );
    const client = await flagsClient();

    const details = await client.getBooleanDetails("new-search", false);

    expect(details.value).toBe(false);
    expect(details.errorCode).toBe("TYPE_MISMATCH");
  });

  it("sends PostHog nothing but the flag request", async () => {
    vi.useFakeTimers();
    const fetchMock = stubPostHog(() =>
      Promise.resolve(flagsResponse({ "new-search": true })),
    );
    const client = await flagsClient();

    await client.getBooleanValue("new-search", false);
    for (let second = 0; second < 60; second += 1) {
      await vi.advanceTimersByTimeAsync(1_000);
    }

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(requestsTo(fetchMock)[0]?.url).toContain("/flags/");
  });

  it("turns every flag off and reports it when PostHog fails, without retrying", async () => {
    vi.useFakeTimers();
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const fetchMock = stubPostHog(() =>
      Promise.resolve(new Response("bad gateway", { status: 502 })),
    );
    const client = await flagsClient();

    const answer = client.getBooleanValue("new-search", false);
    await vi.advanceTimersByTimeAsync(10_000);

    await expect(answer).resolves.toBe(false);
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(consoleError).toHaveBeenCalledWith(
      "Feature flags could not be evaluated",
      expect.anything(),
    );
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
    const client = await flagsClient();

    const check = {
      settled: false,
      result: client.getBooleanValue("new-search", false),
    };
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

  it("turns every flag off and reports it when the PostHog client throws", async () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const failure = new Error("client bug");
    vi.spyOn(PostHog.prototype, "evaluateFlags").mockRejectedValue(failure);
    const client = await flagsClient();

    expect(await client.getBooleanValue("new-search", false)).toBe(false);
    expect(consoleError).toHaveBeenCalledWith(
      "Feature flags could not be evaluated",
      failure,
    );
  });
});
