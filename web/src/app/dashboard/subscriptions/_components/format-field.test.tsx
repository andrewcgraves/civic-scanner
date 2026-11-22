import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PreviewInput } from "../actions";
import { STARTER_TEMPLATE } from "../custom-template";

const mocks = vi.hoisted(() => ({ calls: [] as PreviewInput[] }));

vi.mock("../actions", () => ({
  previewTemplate: (input: PreviewInput) => {
    mocks.calls.push(input);
    return Promise.resolve({
      kind: "rendered",
      type: "agenda_published",
      body: "{}",
    });
  },
}));

const { FormatField } = await import("./format-field");
const { PREVIEW_DELAY_MS } = await import("./template-preview");

const AGENCIES = [
  { slug: "la-metro", name: "LA Metro" },
  { slug: "octa", name: "OCTA" },
];

function Field({
  format = "standard",
  template = "",
  keepsTemplate = false,
}: {
  format?: string;
  template?: string;
  keepsTemplate?: boolean;
}) {
  return (
    <form>
      <select name="agency" aria-label="Agency" defaultValue="la-metro">
        <option value="la-metro">LA Metro</option>
        <option value="octa">OCTA</option>
      </select>
      <FormatField
        defaultFormat={format}
        defaultTemplate={template}
        savedProblem={undefined}
        error={undefined}
        agency="la-metro"
        agencies={AGENCIES}
        keepsTemplate={keepsTemplate}
      />
    </form>
  );
}

function radio(name: string) {
  return screen.getByRole("radio", { name: new RegExp(`^${name}`) });
}

describe("FormatField", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.calls = [];
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("describes each format and marks the chosen one", () => {
    render(<Field format="slack" />);

    expect(radio("Slack")).toBeChecked();
    expect(radio("Slack")).toHaveAccessibleName(
      "Slack A ready-made message for a Slack channel.",
    );
    expect(radio("Slack").closest("label")).toHaveClass("border-accent");
    expect(radio("Discord").closest("label")).not.toHaveClass("border-accent");
  });

  it("opens the editor on an example template when custom is chosen", async () => {
    render(<Field />);
    expect(screen.queryByLabelText("Template")).toBeNull();

    fireEvent.click(radio("Custom JSON"));

    expect(screen.getByLabelText("Template")).toHaveValue(STARTER_TEMPLATE);
    expect(screen.getByRole("heading", { name: "Preview" })).toBeVisible();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREVIEW_DELAY_MS);
    });
    expect(mocks.calls).toEqual([
      {
        template: STARTER_TEMPLATE,
        type: "agenda_published",
        agency: "la-metro",
      },
    ]);
  });

  it("previews for the agency chosen in the form", async () => {
    render(<Field format="custom" template='{"a": "{{agency.name}}"}' />);

    fireEvent.change(screen.getByLabelText("Agency"), {
      target: { value: "octa" },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREVIEW_DELAY_MS);
    });

    expect(mocks.calls.at(-1)).toMatchObject({ agency: "octa" });
    expect(screen.getByRole("status")).toHaveTextContent("Rendered for OCTA");
  });

  it("keeps what was typed when switching away and back", () => {
    render(<Field format="custom" template="{}" />);
    fireEvent.change(screen.getByLabelText("Template"), {
      target: { value: '{"b": 1}' },
    });

    fireEvent.click(radio("Discord"));
    expect(screen.queryByLabelText("Template")).toBeNull();
    fireEvent.click(radio("Custom JSON"));

    expect(screen.getByLabelText("Template")).toHaveValue('{"b": 1}');
  });

  it("says a saved template is kept only when editing one", () => {
    const { rerender } = render(<Field template="{}" keepsTemplate />);
    expect(screen.getByText(/Your custom template is kept/)).toBeVisible();

    rerender(<Field template="{}" />);
    expect(screen.queryByText(/Your custom template is kept/)).toBeNull();
  });
});
