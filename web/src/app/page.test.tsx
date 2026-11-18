import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import HomePage from "./page";

describe("HomePage", () => {
  it("links to the agencies page", () => {
    render(<HomePage />);

    expect(
      screen.getByRole("heading", { name: "Civic Scanner" }),
    ).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Browse agencies" }),
    ).toHaveAttribute("href", "/agencies");
  });
});
