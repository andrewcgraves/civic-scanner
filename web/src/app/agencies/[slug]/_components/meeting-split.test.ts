import { describe, expect, it } from "vitest";

import { isUpcomingMeeting, splitMeetings } from "./meeting-split";

const NOW = new Date("2027-01-15T18:00:00Z");

function meeting(
  id: string,
  startsAt: string,
  status: "scheduled" | "held" | "cancelled",
) {
  return { id, title: id, startsAt, status };
}

describe("isUpcomingMeeting", () => {
  it("is upcoming from its start time onwards unless it was held", () => {
    expect(
      isUpcomingMeeting(meeting("a", NOW.toISOString(), "scheduled"), NOW),
    ).toBe(true);
    expect(
      isUpcomingMeeting(meeting("b", "2027-01-20T17:00:00Z", "cancelled"), NOW),
    ).toBe(true);
    expect(
      isUpcomingMeeting(meeting("c", "2027-01-20T17:00:00Z", "held"), NOW),
    ).toBe(false);
    expect(
      isUpcomingMeeting(meeting("d", "2027-01-15T17:59:59Z", "scheduled"), NOW),
    ).toBe(false);
  });
});

describe("splitMeetings", () => {
  it("orders upcoming soonest first and past most recent first", () => {
    const newestFirst = [
      meeting("late", "2027-03-01T17:00:00Z", "scheduled"),
      meeting("soon", "2027-01-20T17:00:00Z", "cancelled"),
      meeting("held", "2027-01-16T17:00:00Z", "held"),
      meeting("last-week", "2027-01-08T17:00:00Z", "scheduled"),
      meeting("last-month", "2026-12-10T17:00:00Z", "held"),
    ];

    const { upcoming, past } = splitMeetings(newestFirst, NOW);

    expect(upcoming.map((m) => m.id)).toEqual(["soon", "late"]);
    expect(past.map((m) => m.id)).toEqual(["held", "last-week", "last-month"]);
  });
});
