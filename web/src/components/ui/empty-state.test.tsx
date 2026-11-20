import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { EmptyState } from "./empty-state";

describe("EmptyState", () => {
  it("renders the title alone", () => {
    const { container } = render(<EmptyState title="No meetings yet." />);

    expect(screen.getByText("No meetings yet.")).toBeVisible();
    expect(container.firstElementChild?.children).toHaveLength(1);
  });

  it("renders an explanation", () => {
    render(
      <EmptyState title="No meetings yet.">
        New meetings appear after the daily check.
      </EmptyState>,
    );

    expect(
      screen.getByText("New meetings appear after the daily check."),
    ).toBeVisible();
  });
});
