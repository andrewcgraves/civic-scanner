import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import {
  DATE_PASSED_LABEL,
  meetingDisplay,
  MeetingStatusBadge,
  UNKNOWN_STATUS_LABEL,
} from "./meeting-status-badge";

const NOW = new Date("2027-01-20T17:00:00Z");
const PAST = "2027-01-20T16:59:59Z";
const FUTURE = "2027-01-20T17:00:01Z";

function renderBadge(status: string, startsAt: string) {
  render(
    <MeetingStatusBadge display={meetingDisplay(status, startsAt, NOW)} />,
  );
}

describe("meetingDisplay", () => {
  it.each([
    ["one second after now", FUTURE],
    ["exactly now", NOW.toISOString()],
  ])("keeps a scheduled meeting starting %s scheduled", (_when, startsAt) => {
    expect(meetingDisplay("scheduled", startsAt, NOW)).toEqual({
      kind: "scheduled",
      label: "Scheduled",
      tone: "scheduled",
    });
  });

  it("shows a scheduled meeting that started one second ago as date passed", () => {
    expect(meetingDisplay("scheduled", PAST, NOW)).toEqual({
      kind: "date-passed",
      label: DATE_PASSED_LABEL,
      tone: "neutral",
    });
  });

  it("does not claim a scheduled meeting with an invalid date is scheduled or passed", () => {
    expect(meetingDisplay("scheduled", "not a date", NOW)).toEqual({
      kind: "other",
      label: UNKNOWN_STATUS_LABEL,
      tone: "neutral",
    });
  });

  it.each([
    ["held", "Held"],
    ["cancelled", "Cancelled"],
  ] as const)(
    "leaves %s meetings unchanged, past or future",
    (status, label) => {
      for (const startsAt of [PAST, FUTURE, "not a date"]) {
        expect(meetingDisplay(status, startsAt, NOW)).toEqual({
          kind: status,
          label,
          tone: status,
        });
      }
    },
  );

  it.each([
    ["postponed", "Postponed"],
    ["in_recess", "In recess"],
    ["<b>odd</b>", UNKNOWN_STATUS_LABEL],
    ["", UNKNOWN_STATUS_LABEL],
    ["constructor", "Constructor"],
    ["date_passed", "Date passed"],
  ])("labels the unknown status %j as %j, past or future", (status, label) => {
    for (const startsAt of [PAST, FUTURE]) {
      expect(meetingDisplay(status, startsAt, NOW)).toEqual({
        kind: "other",
        label,
        tone: "neutral",
      });
    }
  });
});

describe("MeetingStatusBadge", () => {
  it.each([
    ["scheduled", FUTURE, "Scheduled", "bg-scheduled-surface"],
    ["scheduled", PAST, "Date passed", "bg-surface-muted"],
    ["held", PAST, "Held", "bg-held-surface"],
    ["cancelled", FUTURE, "Cancelled", "bg-cancelled-surface"],
    ["postponed", FUTURE, "Postponed", "bg-surface-muted"],
  ])(
    "shows a %s meeting starting %s as %j",
    (status, startsAt, label, surface) => {
      renderBadge(status, startsAt);

      const badge = screen.getByText(label);
      expect(badge).toHaveClass(surface);
      expect(badge.className).not.toContain("undefined");
    },
  );
});
