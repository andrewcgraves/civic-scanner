import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { MeetingStatusBadge } from "./meeting-status-badge";

describe("MeetingStatusBadge", () => {
  it.each([
    ["scheduled", "Scheduled", "bg-scheduled-surface"],
    ["held", "Held", "bg-held-surface"],
    ["cancelled", "Cancelled", "bg-cancelled-surface"],
  ] as const)("labels %s meetings", (status, label, surface) => {
    render(<MeetingStatusBadge status={status} />);

    expect(screen.getByText(label)).toHaveClass(surface);
  });

  it.each([
    ["postponed", "Postponed"],
    ["in_recess", "In recess"],
    ["<b>odd</b>", "Status unknown"],
    ["", "Status unknown"],
    ["constructor", "Constructor"],
  ])("shows the unknown status %j neutrally as %j", (status, label) => {
    render(<MeetingStatusBadge status={status} />);

    const badge = screen.getByText(label);
    expect(badge).toHaveClass("bg-surface-muted");
    expect(badge.className).not.toContain("undefined");
  });
});
