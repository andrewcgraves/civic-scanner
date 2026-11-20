import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  loadAgencyMeetings: vi.fn(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
}));

vi.mock("next/navigation", () => ({ notFound: mocks.notFound }));
vi.mock("@/lib/api/load", () => ({
  loadAgencyMeetings: mocks.loadAgencyMeetings,
  REQUEST_TIMEOUT_MS: 10_000,
}));

import {
  MAX_FIRST_PAGE_REQUESTS,
  agencyPageHref,
  loadMeetingPage,
  parseCursor,
} from "./meeting-pages";

const NOW = new Date("2027-01-15T18:00:00Z");

function upcoming(n: number) {
  return {
    id: `up-${String(n)}`,
    title: "Board",
    startsAt: "2027-02-01T17:00:00Z",
    status: "scheduled" as const,
  };
}

const PAST = {
  id: "past",
  title: "Board",
  startsAt: "2026-12-01T17:00:00Z",
  status: "held" as const,
};

const page = (meetings: unknown[], nextCursor: string | null) => ({
  ok: true,
  data: { meetings, nextCursor },
});

afterEach(() => {
  mocks.loadAgencyMeetings.mockReset();
});

describe("parseCursor", () => {
  it("accepts no cursor and URL-safe base64 cursors", () => {
    expect(parseCursor(undefined)).toBeUndefined();
    expect(parseCursor("eyJhIjoiYiJ9_-x")).toBe("eyJhIjoiYiJ9_-x");
  });

  it("is not found for anything else", () => {
    for (const value of ["", "a b", "a+b/c=", "x".repeat(513), ["a", "b"]]) {
      expect(() => parseCursor(value)).toThrow("NEXT_NOT_FOUND");
    }
  });
});

describe("agencyPageHref", () => {
  it("links to the agency page with an optional cursor", () => {
    expect(agencyPageHref("la-metro")).toBe("/agencies/la-metro");
    expect(agencyPageHref("la-metro", "abc_-1")).toBe(
      "/agencies/la-metro?cursor=abc_-1",
    );
  });

  it("encodes the slug", () => {
    expect(agencyPageHref("../x?y#z")).toBe("/agencies/..%2Fx%3Fy%23z");
  });
});

const result = (
  meetings: unknown[],
  nextCursor: string | null,
  { incomplete = false, moreUpcoming = false } = {},
) => ({
  ok: true,
  data: { meetings, nextCursor, incomplete, moreUpcoming },
});

function cursorsRequested() {
  return mocks.loadAgencyMeetings.mock.calls.map((call) => call[1] as unknown);
}

describe("loadMeetingPage", () => {
  it("loads only the requested page for a cursor", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page([PAST], "c2"));

    expect(await loadMeetingPage("la-metro", "c1", NOW)).toEqual(
      result([PAST], "c2"),
    );
    expect(cursorsRequested()).toEqual(["c1"]);
  });

  it("says when an older page ends with upcoming meetings and has more", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page([upcoming(1)], "c2"));

    expect(await loadMeetingPage("la-metro", "c1", NOW)).toEqual(
      result([upcoming(1)], "c2", { moreUpcoming: true }),
    );
    expect(mocks.loadAgencyMeetings).toHaveBeenCalledOnce();
  });

  it("passes on an unavailable first page", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue({ ok: false });

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual({
      ok: false,
    });
  });

  it("stops after the first page when it reaches a past meeting", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page([upcoming(1), PAST], "c"));

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual(
      result([upcoming(1), PAST], "c"),
    );
    expect(mocks.loadAgencyMeetings).toHaveBeenCalledOnce();
  });

  it("follows pages of upcoming meetings up to the cap and says more remain", async () => {
    for (let n = 1; n <= MAX_FIRST_PAGE_REQUESTS; n++) {
      mocks.loadAgencyMeetings.mockResolvedValueOnce(
        page([upcoming(n)], `c${String(n)}`),
      );
    }

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual(
      result(
        [1, 2, 3, 4].map(upcoming),
        `c${String(MAX_FIRST_PAGE_REQUESTS)}`,
        {
          moreUpcoming: true,
        },
      ),
    );
    expect(cursorsRequested()).toEqual([undefined, "c1", "c2", "c3"]);
  });

  it("shares one deadline across every request of the first page", async () => {
    const deadline = new AbortController().signal;
    mocks.loadAgencyMeetings
      .mockResolvedValueOnce(page([upcoming(1)], "c1"))
      .mockResolvedValueOnce(page([PAST], null));

    await loadMeetingPage("la-metro", undefined, NOW, deadline);

    expect(mocks.loadAgencyMeetings.mock.calls).toEqual([
      ["la-metro", undefined, undefined, deadline],
      ["la-metro", "c1", undefined, deadline],
    ]);
  });

  it("starts its own deadline when none is given", async () => {
    mocks.loadAgencyMeetings
      .mockResolvedValueOnce(page([upcoming(1)], "c1"))
      .mockResolvedValueOnce(page([PAST], null));

    await loadMeetingPage("la-metro", undefined, NOW);

    const [first, second] = mocks.loadAgencyMeetings.mock.calls.map(
      (call) => call[3] as unknown,
    );
    expect(first).toBeInstanceOf(AbortSignal);
    expect(second).toBe(first);
  });

  it("keeps what loaded when the deadline cuts off a further page", async () => {
    const controller = new AbortController();
    mocks.loadAgencyMeetings
      .mockResolvedValueOnce(page([upcoming(1)], "c1"))
      .mockImplementationOnce(() => {
        controller.abort();
        return Promise.resolve({ ok: false });
      });

    expect(
      await loadMeetingPage("la-metro", undefined, NOW, controller.signal),
    ).toEqual(
      result([upcoming(1)], "c1", { incomplete: true, moreUpcoming: true }),
    );
    expect(mocks.loadAgencyMeetings).toHaveBeenCalledTimes(2);
  });

  it("stops at the last page", async () => {
    mocks.loadAgencyMeetings
      .mockResolvedValueOnce(page([upcoming(1)], "c1"))
      .mockResolvedValueOnce(page([upcoming(2)], null));

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual(
      result([upcoming(1), upcoming(2)], null),
    );
  });

  it("stops on an empty first page", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page([], "c1"));

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual(
      result([], "c1"),
    );
    expect(mocks.loadAgencyMeetings).toHaveBeenCalledOnce();
  });
});
