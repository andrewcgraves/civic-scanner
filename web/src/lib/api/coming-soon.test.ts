import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ loadAgencyMeetings: vi.fn() }));

vi.mock("./load", () => ({
  loadAgencyMeetings: mocks.loadAgencyMeetings,
  REQUEST_TIMEOUT_MS: 10_000,
}));

import { findComingSoon, MAX_COMING_SOON_LOOKUPS } from "./coming-soon";

function agency(slug: string) {
  return { id: `id-${slug}`, slug, name: slug, website: "https://example.org" };
}

const page = (count: number) => ({
  ok: true,
  data: {
    meetings: Array.from({ length: count }, () => ({})),
    nextCursor: null,
  },
});

afterEach(() => {
  mocks.loadAgencyMeetings.mockReset();
});

describe("findComingSoon", () => {
  it("marks agencies whose first page of meetings is empty", async () => {
    mocks.loadAgencyMeetings.mockImplementation((slug: string) =>
      Promise.resolve(page(slug === "rctc" ? 0 : 1)),
    );

    const result = await findComingSoon([agency("la-metro"), agency("rctc")]);

    expect([...result]).toEqual(["id-rctc"]);
  });

  it("asks for one meeting per agency, all under one shared deadline", async () => {
    const deadline = new AbortController().signal;
    mocks.loadAgencyMeetings.mockResolvedValue(page(1));

    await findComingSoon([agency("a"), agency("b")], deadline);

    expect(mocks.loadAgencyMeetings.mock.calls).toEqual([
      ["a", undefined, 1, deadline],
      ["b", undefined, 1, deadline],
    ]);
  });

  it("starts its own deadline when none is given", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page(1));

    await findComingSoon([agency("a"), agency("b")]);

    const [first, second] = mocks.loadAgencyMeetings.mock.calls.map(
      (call) => call[3] as unknown,
    );
    expect(first).toBeInstanceOf(AbortSignal);
    expect(second).toBe(first);
  });

  it("does not mark an agency whose lookup fails or throws", async () => {
    mocks.loadAgencyMeetings
      .mockResolvedValueOnce({ ok: false })
      .mockRejectedValueOnce(new Error("NEXT_NOT_FOUND"));

    const result = await findComingSoon([agency("a"), agency("b")]);

    expect(result.size).toBe(0);
  });

  it("skips the lookups for more agencies than the cap", async () => {
    const many = Array.from({ length: MAX_COMING_SOON_LOOKUPS + 1 }, (_, i) =>
      agency(`a${String(i)}`),
    );

    expect((await findComingSoon(many)).size).toBe(0);
    expect(mocks.loadAgencyMeetings).not.toHaveBeenCalled();
  });
});
