import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PROMPT_EXAMPLES } from "../state";

import { PromptField } from "./prompt-field";

function field() {
  return screen.getByLabelText(/Directed prompt/);
}

function counter() {
  return screen.getByText(/ \/ 500 characters$/);
}

describe("PromptField", () => {
  it("names the field, says it is optional and counts nothing yet", () => {
    render(<PromptField defaultValue="" error={undefined} editing={false} />);

    expect(field()).toHaveAttribute("name", "prompt");
    expect(field()).toHaveAttribute("id", "prompt");
    expect(field()).not.toHaveAttribute("aria-invalid");
    expect(field()).not.toHaveAttribute("maxLength");
    expect(screen.getByText("(optional)")).toBeVisible();
    expect(counter()).toHaveTextContent("0 / 500 characters");
    expect(counter()).not.toHaveClass("text-error-ink");
  });

  it("counts characters after trimming as the user types", () => {
    render(<PromptField defaultValue="" error={undefined} editing={false} />);

    fireEvent.change(field(), { target: { value: "  Fares 🚆\r\n " } });

    expect(counter()).toHaveTextContent("7 / 500 characters");
  });

  it("marks the counter past the limit without blocking the typing", () => {
    render(<PromptField defaultValue="" error={undefined} editing={false} />);

    fireEvent.change(field(), { target: { value: "a".repeat(537) } });

    expect(field()).toHaveValue("a".repeat(537));
    expect(counter()).toHaveTextContent("537 / 500 characters");
    expect(counter()).toHaveClass("text-error-ink", "font-medium");
  });

  it("fills the field from an example and hides the examples", () => {
    render(<PromptField defaultValue="" error={undefined} editing={false} />);
    const [, rail] = PROMPT_EXAMPLES;

    fireEvent.click(screen.getByRole("button", { name: "One rail line" }));

    expect(field()).toHaveValue(rail?.text);
    expect(field()).toHaveFocus();
    expect(screen.queryByRole("button", { name: "Fare changes" })).toBeNull();
    expect(counter()).toHaveTextContent(
      `${String(rail?.text.length)} / 500 characters`,
    );
  });

  it("brings the examples back when the field is cleared", () => {
    render(
      <PromptField defaultValue="Fare changes" error={undefined} editing />,
    );
    expect(screen.queryByRole("button", { name: "Fare changes" })).toBeNull();

    fireEvent.change(field(), { target: { value: "   " } });

    expect(
      screen.getByRole("button", { name: "Fare changes" }),
    ).toHaveAttribute("type", "button");
  });

  it("ties an error to the field", () => {
    render(
      <PromptField
        defaultValue="Fares"
        error="The prompt can be at most 500 characters. Take out 3."
        editing
      />,
    );

    expect(field()).toHaveValue("Fares");
    expect(field()).toHaveAttribute("aria-invalid", "true");
    expect(field()).toHaveAccessibleDescription(
      /Clear it to get every event again\. 5 \/ 500 characters The prompt can be at most 500 characters\. Take out 3\./,
    );
  });
});
