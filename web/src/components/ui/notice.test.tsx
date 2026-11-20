import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Notice, UnavailableNotice } from "./notice";

describe("Notice", () => {
  it("is a plain informational note by default", () => {
    render(<Notice>The transcript is not ready yet.</Notice>);

    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("The transcript is not ready yet.")).toBeVisible();
  });

  it("is an alert with a title for errors", () => {
    render(
      <Notice tone="error" title="Something went wrong">
        Details
      </Notice>,
    );

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Something went wrong");
    expect(alert).toHaveTextContent("Details");
    expect(alert).toHaveClass("bg-error-surface");
  });
});

describe("UnavailableNotice", () => {
  it("says what is unavailable", () => {
    render(<UnavailableNotice what="Meetings are" />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Meetings are unavailable right now. Please try again later.",
    );
  });
});
