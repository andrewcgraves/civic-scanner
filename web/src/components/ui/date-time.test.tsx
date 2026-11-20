import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { MeetingTime, Offset } from "./date-time";

const ISO = "2027-01-20T17:00:00Z";

describe("MeetingTime", () => {
  it("renders the long date and time in a time element by default", () => {
    render(<MeetingTime value={ISO} />);

    const time = screen.getByText("Wednesday, January 20, 2027 at 9:00 AM PST");
    expect(time.tagName).toBe("TIME");
    expect(time).toHaveAttribute("dateTime", ISO);
  });

  it.each([
    ["date", "Wednesday, January 20, 2027"],
    ["time", "9:00 AM PST"],
    ["short-date", "Wed, Jan 20, 2027"],
    ["short-datetime", "Wed, Jan 20, 2027, 9:00 AM PST"],
  ] as const)("renders the %s format", (format, text) => {
    render(<MeetingTime value={ISO} format={format} />);

    expect(screen.getByText(text)).toHaveAttribute("dateTime", ISO);
  });
});

describe("Offset", () => {
  it("renders a transcript offset with a duration", () => {
    render(<Offset ms={3_723_000} />);

    const time = screen.getByText("1:02:03");
    expect(time.tagName).toBe("TIME");
    expect(time).toHaveAttribute("dateTime", "PT1H2M3S");
  });
});
