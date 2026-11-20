import { describe, expect, it } from "vitest";

import {
  formatMeetingDate,
  formatMeetingDateTime,
  formatMeetingTime,
  formatOffset,
  formatShortDate,
  formatShortDateTime,
  isUpcoming,
  MEETING_TIME_ZONE,
  offsetDuration,
  UNKNOWN_DATE,
} from "./datetime";

const WINTER = "2027-01-20T17:00:00Z";
const SUMMER = "2026-07-04T02:05:00Z";

describe("meeting dates", () => {
  it("are shown in Los Angeles time", () => {
    expect(MEETING_TIME_ZONE).toBe("America/Los_Angeles");
  });

  it("formats the long date", () => {
    expect(formatMeetingDate(WINTER)).toBe("Wednesday, January 20, 2027");
  });

  it("uses the Los Angeles calendar day, not the UTC one", () => {
    expect(formatMeetingDate(SUMMER)).toBe("Friday, July 3, 2026");
  });

  it("formats the time with standard and daylight zone abbreviations", () => {
    expect(formatMeetingTime(WINTER)).toBe("9:00 AM PST");
    expect(formatMeetingTime(SUMMER)).toBe("7:05 PM PDT");
  });

  it("uses plain spaces only, so server and browser output match", () => {
    expect(formatMeetingDateTime(WINTER)).toMatch(/^[\x20-\x7e]+$/);
  });

  it("formats the long date and time", () => {
    expect(formatMeetingDateTime(WINTER)).toBe(
      "Wednesday, January 20, 2027 at 9:00 AM PST",
    );
  });

  it("formats the short date and date-time", () => {
    expect(formatShortDate(WINTER)).toBe("Wed, Jan 20, 2027");
    expect(formatShortDateTime(SUMMER)).toBe("Fri, Jul 3, 2026, 7:05 PM PDT");
  });

  it("formats noon and midnight on the twelve-hour clock", () => {
    expect(formatMeetingTime("2026-11-19T20:00:00Z")).toBe("12:00 PM PST");
    expect(formatMeetingTime("2026-11-19T08:00:00Z")).toBe("12:00 AM PST");
  });
});

describe("invalid dates", () => {
  it("format as an unknown date instead of throwing", () => {
    for (const format of [
      formatMeetingDate,
      formatMeetingTime,
      formatMeetingDateTime,
      formatShortDate,
      formatShortDateTime,
    ]) {
      expect(format("not a date")).toBe(UNKNOWN_DATE);
    }
  });

  it("are never upcoming", () => {
    expect(isUpcoming("not a date", new Date("2027-01-01T00:00:00Z"))).toBe(
      false,
    );
  });
});

describe("isUpcoming", () => {
  const now = new Date("2026-11-19T12:00:00Z");

  it("is true for a meeting starting now or later", () => {
    expect(isUpcoming("2026-11-19T12:00:00Z", now)).toBe(true);
    expect(isUpcoming(WINTER, now)).toBe(true);
  });

  it("is false for a meeting that already started", () => {
    expect(isUpcoming(SUMMER, now)).toBe(false);
  });
});

describe("transcript offsets", () => {
  it("formats minutes and seconds under an hour", () => {
    expect(formatOffset(0)).toBe("0:00");
    expect(formatOffset(62_999)).toBe("1:02");
    expect(formatOffset(3_599_000)).toBe("59:59");
  });

  it("formats hours, minutes and seconds from an hour on", () => {
    expect(formatOffset(3_723_000)).toBe("1:02:03");
    expect(formatOffset(36_000_000)).toBe("10:00:00");
  });

  it("treats a negative offset as the start", () => {
    expect(formatOffset(-5)).toBe("0:00");
    expect(offsetDuration(-5)).toBe("PT0H0M0S");
  });

  it("gives a machine-readable duration", () => {
    expect(offsetDuration(3_723_456)).toBe("PT1H2M3S");
  });
});
