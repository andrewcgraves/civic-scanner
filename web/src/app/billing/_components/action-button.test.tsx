import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ActionButton } from "./action-button";

describe("ActionButton", () => {
  it("is a submit button inside its own form", () => {
    render(<ActionButton action={() => Promise.resolve()}>Go</ActionButton>);

    const button = screen.getByRole("button", { name: "Go" });
    expect(button).toHaveAttribute("type", "submit");
    expect(button.closest("form")).not.toBeNull();
  });
});
