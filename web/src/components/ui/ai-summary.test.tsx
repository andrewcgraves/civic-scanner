import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AiSummary, DEFAULT_AI_NOTE, paragraphsOf } from "./ai-summary";

describe("AiSummary", () => {
  it("labels the text as AI-generated and adds the default note", () => {
    render(<AiSummary text="The board approves the budget." />);

    expect(screen.getByText("AI-generated")).toBeVisible();
    expect(screen.getByText("The board approves the budget.")).toBeVisible();
    expect(screen.getByText(DEFAULT_AI_NOTE)).toBeVisible();
  });

  it("renders each line as a paragraph", () => {
    const { container } = render(
      <AiSummary
        text={
          "First part.\n\nSecond part.\nThis summary covers only the first part of the agenda."
        }
      />,
    );

    expect(container.querySelectorAll(".prose p")).toHaveLength(3);
  });

  it("escapes markup in the text", () => {
    const { container } = render(<AiSummary text="<b>bold</b>" />);

    expect(container.querySelector(".prose b")).toBeNull();
    expect(screen.getByText("<b>bold</b>")).toBeVisible();
  });

  it("takes a custom note", () => {
    render(
      <AiSummary
        text="x"
        note={<a href="https://a.example">Read the agenda</a>}
      />,
    );

    expect(screen.getByRole("link", { name: "Read the agenda" })).toBeVisible();
    expect(screen.queryByText(DEFAULT_AI_NOTE)).toBeNull();
  });
});

describe("paragraphsOf", () => {
  it("drops blank lines and trims", () => {
    expect(paragraphsOf("  a \n\n \n b")).toEqual(["a", "b"]);
  });
});
