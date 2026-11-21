import { describe, expect, it } from "vitest";

import {
  DEFAULT_EVENT_TYPE,
  EVENT_TYPES,
  IDLE_TEST,
  attemptCount,
  attemptResult,
  deliveriesPath,
  eventLabel,
  failureStreak,
  formatDuration,
  isEventType,
  problem,
  readCursor,
  readRetryAfter,
} from "./deliveries";

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";

describe("event types", () => {
  it("offers every type and starts with an agenda being published", () => {
    expect(EVENT_TYPES).toEqual([
      "agenda_published",
      "agenda_changed",
      "transcript_ready",
    ]);
    expect(DEFAULT_EVENT_TYPE).toBe("agenda_published");
    expect(IDLE_TEST).toEqual({ kind: "idle" });
  });

  it("names known types and falls back to the raw value", () => {
    expect(eventLabel("agenda_changed")).toBe("Agenda changed");
    expect(eventLabel("transcript_ready")).toBe("Transcript ready");
    expect(eventLabel("meeting_moved")).toBe("meeting_moved");
    expect(isEventType("agenda_published")).toBe(true);
    expect(isEventType(null)).toBe(false);
  });
});

describe("readRetryAfter", () => {
  it.each<[string | null, number]>([
    ["42", 42],
    [" 7 ", 7],
    ["0", 1],
    ["99999", 3600],
    ["soon", 60],
    ["1.5", 60],
    ["", 60],
    [null, 60],
  ])("reads %j as %d seconds", (value, seconds) => {
    expect(readRetryAfter(value)).toBe(seconds);
  });
});

describe("readCursor and deliveriesPath", () => {
  it("takes a single, reasonably sized cursor", () => {
    expect(readCursor({ cursor: "abc" })).toBe("abc");
    expect(readCursor({ cursor: "" })).toBeUndefined();
    expect(readCursor({ cursor: ["a", "b"] })).toBeUndefined();
    expect(readCursor({ cursor: "x".repeat(1025) })).toBeUndefined();
    expect(readCursor({})).toBeUndefined();
  });

  it("puts the cursor in the subscription page's query", () => {
    expect(deliveriesPath(ID, "a+b/c")).toBe(
      `/dashboard/subscriptions/${ID}?cursor=a%2Bb%2Fc`,
    );
  });
});

describe("formatting", () => {
  it("gives durations in milliseconds below a second, else seconds", () => {
    expect(formatDuration(0)).toBe("0 ms");
    expect(formatDuration(182.4)).toBe("182 ms");
    expect(formatDuration(1204)).toBe("1.2 s");
    expect(formatDuration(10_000)).toBe("10.0 s");
  });

  it("cleans up a problem without an answer", () => {
    expect(problem(" timed out. ")).toBe("timed out");
    expect(problem("x".repeat(400))).toHaveLength(300);
    expect(problem("  ")).toBeUndefined();
    expect(problem(null)).toBeUndefined();
  });

  it("describes an attempt's result", () => {
    expect(attemptResult({ statusCode: 503, error: null })).toBe(
      "Answered 503",
    );
    expect(
      attemptResult({ statusCode: null, error: "connection refused" }),
    ).toBe("No answer: connection refused");
    expect(attemptResult({ statusCode: null, error: null })).toBe("No answer");
  });

  it("counts attempts and failed events", () => {
    expect(attemptCount(1)).toBe("1 attempt");
    expect(attemptCount(8)).toBe("8 attempts");
    expect(failureStreak(1)).toBe("The last event failed to deliver");
    expect(failureStreak(3)).toBe("The last 3 events failed to deliver");
  });
});
