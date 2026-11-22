import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";

import type { TemplateProblem } from "../custom-template";

import { TemplateEditor } from "./template-editor";

function Editor({
  initial,
  problem,
}: {
  initial: string;
  problem?: TemplateProblem;
}) {
  const [value, setValue] = useState(initial);
  return <TemplateEditor value={value} onChange={setValue} problem={problem} />;
}

function field() {
  return screen.getByLabelText<HTMLTextAreaElement>("Template");
}

describe("TemplateEditor", () => {
  it("is a described code field with the size used", () => {
    render(<Editor initial='{"a": "b"}' />);

    expect(field()).toHaveValue('{"a": "b"}');
    expect(field()).toHaveAttribute("spellcheck", "false");
    expect(field()).not.toHaveAttribute("aria-invalid");
    expect(field()).toHaveAccessibleDescription(
      /up to 8,192 characters.*\{\{meeting\.title\}\}.*10 of 8,192 characters used/,
    );
  });

  it("lists the placeholders by group and inserts one at the cursor", () => {
    render(<Editor initial='{"a": ""}' />);
    const group = screen.getByRole("group", {
      name: "Insert a placeholder at the cursor",
    });
    expect(group).toHaveTextContent(/^Agency.*Meeting.*Event.*Agenda/);
    const title = screen.getByRole("button", {
      name: "Insert {{meeting.title}}",
    });
    expect(title).toHaveTextContent("meeting.title");
    expect(title).toHaveAttribute("title", "Its title");

    field().setSelectionRange(7, 7);
    fireEvent.click(title);

    expect(field()).toHaveValue('{"a": "{{meeting.title}}"}');
    expect(field()).toHaveFocus();
    expect(field().selectionStart).toBe(24);
  });

  it("marks the size over the limit", () => {
    render(<Editor initial={"x".repeat(8200)} />);

    const size = screen.getByText("8,200 of 8,192 characters used");
    expect(size).toHaveClass("text-error-ink");
  });

  it("shows where a JSON problem is and goes there", () => {
    const text = '{\n  "a": "x"\n  "b": 1\n}';
    render(
      <Editor
        initial={text}
        problem={{ message: "The template is not valid JSON.", index: 15 }}
      />,
    );

    expect(field()).toHaveAttribute("aria-invalid", "true");
    expect(field()).toHaveAccessibleDescription(
      /Line 3, column 3: The template is not valid JSON\./,
    );
    expect(screen.getByText(/\|\s+"b": 1/).textContent).toBe(
      '3 |   "b": 1\n  |   ^',
    );

    fireEvent.click(screen.getByRole("button", { name: "Go to line 3" }));

    expect(field()).toHaveFocus();
    expect(field().selectionStart).toBe(15);
    expect(field().selectionEnd).toBe(16);
  });

  it("offers the likely placeholder for an unknown one", () => {
    const text = '{"title": "{{meeting.titel}}"}';
    render(
      <Editor
        initial={text}
        problem={{
          message: "{{meeting.titel}} in “title” is not a placeholder.",
          placeholder: "{{meeting.titel}}",
          index: 11,
          suggestion: "{{meeting.title}}",
        }}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Select it" }));
    expect(field().selectionStart).toBe(11);
    expect(field().selectionEnd).toBe(28);

    fireEvent.click(
      screen.getByRole("button", { name: "Use {{meeting.title}}" }),
    );
    expect(field()).toHaveValue('{"title": "{{meeting.title}}"}');
    expect(screen.queryByRole("button", { name: "Select it" })).toBeNull();
  });

  it("shows a problem without a place as it is", () => {
    render(
      <Editor
        initial="[]"
        problem={{ message: "The template must be a JSON object." }}
      />,
    );

    expect(field()).toHaveAccessibleDescription(
      /characters used The template must be a JSON object\.$/,
    );
    expect(screen.queryByRole("button", { name: /Go to line/ })).toBeNull();
  });
});
