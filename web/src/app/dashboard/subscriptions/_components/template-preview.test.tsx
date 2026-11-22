import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PreviewInput } from "../actions";
import type { PreviewState, TemplateProblem } from "../custom-template";

const mocks = vi.hoisted(() => {
  const unavailable = (): PreviewState | Error => ({ kind: "unavailable" });
  return { calls: [] as PreviewInput[], answer: unavailable() };
});

vi.mock("../actions", () => ({
  previewTemplate: (input: PreviewInput) => {
    mocks.calls.push(input);
    return mocks.answer instanceof Error
      ? Promise.reject(mocks.answer)
      : Promise.resolve(mocks.answer);
  },
}));

const { PREVIEW_DELAY_MS, TemplatePreview, usePreview } =
  await import("./template-preview");

const TEMPLATE = '{"title": "{{meeting.title}}"}';
const PROBLEM: TemplateProblem = {
  message: "The key “a” appears more than once.",
};

function Harness({
  template,
  agency = "la-metro",
  saved = { template: "", problem: undefined },
}: {
  template: string;
  agency?: string;
  saved?: { template: string; problem: TemplateProblem | undefined };
}) {
  const preview = usePreview(template, agency, saved);
  return (
    <>
      <TemplatePreview preview={preview} agencyName="LA Metro" />
      <p data-testid="problem">{preview.problem?.message ?? "none"}</p>
    </>
  );
}

async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(PREVIEW_DELAY_MS);
  });
}

function status() {
  return screen.getByRole("status");
}

describe("TemplatePreview", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.calls = [];
    mocks.answer = {
      kind: "rendered",
      type: "agenda_published",
      body: '{"title":"Test: Agenda published","n":1.50}',
    };
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("waits for typing to stop, then shows the rendered body formatted", async () => {
    const { rerender } = render(<Harness template="{}" />);
    rerender(<Harness template={TEMPLATE} />);

    expect(status()).toHaveTextContent("Checking…");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREVIEW_DELAY_MS - 1);
    });
    expect(mocks.calls).toEqual([]);
    await settle();

    expect(mocks.calls).toEqual([
      { template: TEMPLATE, type: "agenda_published", agency: "la-metro" },
    ]);
    expect(status()).toHaveTextContent(
      "Valid Rendered for LA Metro, marked as a test.",
    );
    expect(screen.getByLabelText("Rendered body").textContent).toBe(
      '{\n  "title": "Test: Agenda published",\n  "n": 1.50\n}',
    );
  });

  it("previews the chosen sample event", async () => {
    render(<Harness template={TEMPLATE} />);
    await settle();

    fireEvent.change(screen.getByLabelText("Sample event"), {
      target: { value: "transcript_ready" },
    });
    expect(screen.getByLabelText("Rendered body")).toHaveClass("opacity-60");
    await settle();

    expect(mocks.calls.at(-1)).toMatchObject({ type: "transcript_ready" });
  });

  it("reports the API's problem with the template", async () => {
    mocks.answer = { kind: "invalid", problem: PROBLEM };
    render(<Harness template={TEMPLATE} />);
    await settle();

    expect(status()).toHaveTextContent(
      "Not valid Fix the template to see the preview.",
    );
    expect(screen.getByTestId("problem")).toHaveTextContent(PROBLEM.message);
    expect(screen.queryByLabelText("Rendered body")).toBeNull();
  });

  it("checks an empty or oversized template without calling the API", async () => {
    const { rerender } = render(<Harness template="  " />);
    await settle();
    expect(screen.getByTestId("problem")).toHaveTextContent(
      "Write a template, or choose another message format.",
    );

    rerender(<Harness template={"x".repeat(8193)} />);
    await settle();
    expect(screen.getByTestId("problem")).toHaveTextContent(
      "The template is 1 character too long.",
    );
    expect(mocks.calls).toEqual([]);
  });

  it("keeps the saved problem until the template changes", () => {
    render(
      <Harness
        template={TEMPLATE}
        saved={{ template: TEMPLATE, problem: PROBLEM }}
      />,
    );

    expect(screen.getByTestId("problem")).toHaveTextContent(PROBLEM.message);
  });

  it("says when it cannot preview, and tries again", async () => {
    mocks.answer = new Error("network");
    render(<Harness template={TEMPLATE} />);
    await settle();

    expect(status()).toHaveTextContent(
      "Unavailable The preview cannot be shown right now.",
    );
    mocks.answer = { kind: "rendered", type: "agenda_published", body: "{}" };
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await settle();

    expect(mocks.calls).toHaveLength(2);
    expect(status()).toHaveTextContent("Valid");
  });
});
