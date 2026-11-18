import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("next/server", () => ({ connection: () => Promise.resolve() }));

function hangingFetch() {
  return vi.fn(
    (_input: RequestInfo | URL, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          reject(new DOMException("aborted", "AbortError"));
        });
      }),
  );
}

async function startFlagCheck(fetchImpl: typeof fetch) {
  vi.stubGlobal("fetch", fetchImpl);
  vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", "phc_test");
  vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", "https://posthog.invalid");
  vi.resetModules();
  const { isFlagEnabled } = await import("./server");
  const check = { settled: false, result: isFlagEnabled("new-search") };
  void check.result.finally(() => {
    check.settled = true;
  });
  return check;
}

describe("isFlagEnabled when PostHog is slow or failing", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it("gives up on an unanswered request after half a second", async () => {
    const fetchMock = hangingFetch();
    const check = await startFlagCheck(fetchMock);

    await vi.advanceTimersByTimeAsync(400);
    expect(check.settled).toBe(false);
    await vi.advanceTimersByTimeAsync(200);

    expect(check.settled).toBe(true);
    await expect(check.result).resolves.toBe(false);
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it("does not retry a failed request", async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve(new Response("bad gateway", { status: 502 })),
    );
    const check = await startFlagCheck(fetchMock);

    await vi.advanceTimersByTimeAsync(10_000);

    await expect(check.result).resolves.toBe(false);
    expect(fetchMock).toHaveBeenCalledOnce();
  });
});
