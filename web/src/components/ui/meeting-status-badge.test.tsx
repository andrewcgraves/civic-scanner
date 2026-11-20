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
});
