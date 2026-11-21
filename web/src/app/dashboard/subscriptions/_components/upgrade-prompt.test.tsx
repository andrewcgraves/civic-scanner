import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/lib/billing/actions", () => ({
  startCheckoutAction: vi.fn(),
}));

const { UpgradePrompt } = await import("./upgrade-prompt");

describe("UpgradePrompt", () => {
  it("offers the paid plan when billing is on", () => {
    render(<UpgradePrompt billing />);

    expect(
      screen.getByText("Subscriptions come with the paid plan"),
    ).toBeVisible();
    expect(screen.getByText("Paid")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Start the paid plan" }),
    ).toBeVisible();
    expect(screen.getByText(/secure Stripe page/)).toBeVisible();
  });

  it("explains, without a button, when billing is off", () => {
    render(<UpgradePrompt billing={false} />);

    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("The paid plan is not open yet.")).toBeVisible();
  });
});
