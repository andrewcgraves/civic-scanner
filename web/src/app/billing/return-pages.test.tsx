import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ enabled: true }));

vi.mock("@/lib/billing/server", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  requireBilling: () =>
    mocks.enabled ? Promise.resolve() : Promise.reject(new Error("not found")),
}));

import CancelPage, { metadata as cancelMetadata } from "./cancel/page";
import SuccessPage, { metadata as successMetadata } from "./success/page";

describe("billing return pages", () => {
  beforeEach(() => {
    mocks.enabled = true;
  });

  it("says the payment is being confirmed, not that the plan is active", async () => {
    render(await SuccessPage());

    expect(successMetadata).toEqual({ title: "Confirming your payment" });
    expect(
      screen.getByRole("heading", {
        level: 1,
        name: "Confirming your payment",
      }),
    ).toBeVisible();
    expect(screen.getByText("Your plan is not active yet")).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Back to billing" }),
    ).toHaveAttribute("href", "/billing");
  });

  it("says a cancelled checkout charged nothing", async () => {
    render(await CancelPage());

    expect(cancelMetadata).toEqual({ title: "Checkout cancelled" });
    expect(
      screen.getByText("You have not been charged and nothing has changed."),
    ).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Back to billing" }),
    ).toHaveAttribute("href", "/billing");
  });

  it("are 404s when billing is off", async () => {
    mocks.enabled = false;

    await expect(SuccessPage()).rejects.toThrow("not found");
    await expect(CancelPage()).rejects.toThrow("not found");
  });
});
