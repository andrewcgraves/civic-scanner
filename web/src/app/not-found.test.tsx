import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import NotFound, { metadata } from "./not-found";

describe("NotFound", () => {
  it("is titled Page not found", () => {
    expect(metadata.title).toBe("Page not found");
  });

  it("says the page was not found and links onward", () => {
    render(<NotFound />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Page not found" }),
    ).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Go to the home page" }),
    ).toHaveAttribute("href", "/");
    expect(
      screen.getByRole("link", { name: "browse agencies" }),
    ).toHaveAttribute("href", "/agencies");
  });
});
