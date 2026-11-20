import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const status = vi.hoisted(() => ({ pending: false }));

vi.mock("react-dom", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useFormStatus: () => ({ pending: status.pending }),
}));

import { ActionButton } from "./action-button";

describe("ActionButton", () => {
  beforeEach(() => {
    status.pending = false;
  });

  it("is an enabled submit button inside its own form", () => {
    render(<ActionButton action={() => Promise.resolve()}>Go</ActionButton>);

    const button = screen.getByRole("button", { name: "Go" });
    expect(button).toHaveAttribute("type", "submit");
    expect(button).toBeEnabled();
    expect(button).toHaveAttribute("aria-busy", "false");
    expect(button.closest("form")).not.toBeNull();
  });

  it("disables itself while the form is submitting", () => {
    status.pending = true;

    render(<ActionButton action={() => Promise.resolve()}>Go</ActionButton>);

    const button = screen.getByRole("button", { name: "Opening Stripe…" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
  });
});
