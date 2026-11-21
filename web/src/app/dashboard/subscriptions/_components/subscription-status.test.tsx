import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { FailureStreak, StatusBadge } from "./subscription-status";

describe("StatusBadge", () => {
  it.each([
    [{ status: "active", pausedReason: null }, "Active"],
    [{ status: "paused", pausedReason: "owner" }, "Paused"],
    [{ status: "paused", pausedReason: null }, "Paused"],
    [
      { status: "paused", pausedReason: "failed_deliveries" },
      "Paused: deliveries failing",
    ],
  ] as const)("labels %j as %s", (subscription, label) => {
    const { container } = render(<StatusBadge subscription={subscription} />);

    expect(container).toHaveTextContent(new RegExp(`^${label}$`));
  });
});

describe("FailureStreak", () => {
  it("says nothing while no events have failed", () => {
    const { container } = render(<FailureStreak count={0} />);

    expect(container).toBeEmptyDOMElement();
  });

  it("warns in a line on the list", () => {
    render(<FailureStreak count={2} compact />);

    expect(
      screen.getByText(
        "The last 2 events failed to deliver. After 5 in a row, this subscription is paused.",
      ),
    ).toBeVisible();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("warns in a notice on the subscription page", () => {
    render(<FailureStreak count={1} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("The last event failed to deliver");
    expect(alert).toHaveTextContent("A delivered event resets the count.");
  });
});
