import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { TestResult, TestState } from "../deliveries";

const mocks = vi.hoisted(() => ({
  next: { kind: "idle" } as TestState,
  calls: [] as Record<string, unknown>[],
}));

vi.mock("../actions", () => ({
  sendTest: (_previous: TestState, formData: FormData) => {
    mocks.calls.push(Object.fromEntries(formData));
    return Promise.resolve(mocks.next);
  },
}));

const { SendTest } = await import("./send-test");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";

function result(overrides: Partial<TestResult> = {}): TestResult {
  return {
    id: "5b1c7d2e-0000-4000-8000-000000000001",
    type: "agenda_published",
    sentAt: "2026-11-20T18:14:00Z",
    succeeded: true,
    statusCode: 200,
    durationMs: 182,
    error: null,
    ...overrides,
  };
}

async function send() {
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Send a test" }));
    await Promise.resolve();
  });
}

describe("SendTest", () => {
  beforeEach(() => {
    mocks.next = { kind: "idle" };
    mocks.calls = [];
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("sends the chosen event type", async () => {
    render(<SendTest id={ID} />);
    const select = screen.getByLabelText("Event");
    expect(select).toHaveValue("agenda_published");
    expect(
      screen.getAllByRole("option").map((option) => option.textContent),
    ).toEqual(["Agenda published", "Agenda changed", "Transcript ready"]);

    fireEvent.change(select, { target: { value: "transcript_ready" } });
    await send();

    expect(mocks.calls).toEqual([{ id: ID, type: "transcript_ready" }]);
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("shows a delivered test", async () => {
    mocks.next = { kind: "sent", result: result() };
    render(<SendTest id={ID} />);

    await send();

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Test delivered");
    expect(status).toHaveTextContent(
      /Your endpoint answered 200 in 182 ms\. Sent Fri, Nov 20, 2026, 10:14.AM PST\./,
    );
  });

  it("shows a test the endpoint refused", async () => {
    mocks.next = {
      kind: "sent",
      result: result({ succeeded: false, statusCode: 500, durationMs: 1204 }),
    };
    render(<SendTest id={ID} />);

    await send();

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Test not delivered");
    expect(status).toHaveTextContent(
      "Your endpoint answered 500 in 1.2 s. Any 2xx counts as delivered.",
    );
  });

  it.each<[string | null, string]>([
    ["connection refused", "No answer: connection refused."],
    [null, "There was no answer."],
  ])("shows a test without an answer (%j)", async (error, text) => {
    mocks.next = {
      kind: "sent",
      result: result({
        succeeded: false,
        statusCode: null,
        durationMs: 0,
        error,
      }),
    };
    render(<SendTest id={ID} />);

    await send();

    expect(screen.getByRole("status")).toHaveTextContent(text);
  });

  it("waits out the test limit before allowing another", async () => {
    vi.useFakeTimers();
    mocks.next = { kind: "limited", retryAfter: 42 };
    render(<SendTest id={ID} />);

    await send();

    expect(screen.getByRole("status")).toHaveTextContent(
      "You have sent 10 tests in the last minute",
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "You can send another in 42 seconds.",
    );
    expect(screen.getByRole("button", { name: "Send a test" })).toBeDisabled();

    act(() => {
      vi.advanceTimersByTime(42_000);
    });

    expect(screen.getByRole("status")).toHaveTextContent(
      "You can send another test now.",
    );
    expect(screen.getByRole("button", { name: "Send a test" })).toBeEnabled();
  });

  it.each<[TestState, RegExp]>([
    [{ kind: "gone" }, /no longer exists/],
    [{ kind: "upgrade" }, /need the paid plan/],
    [{ kind: "unavailable" }, /could not be sent right now/],
  ])("explains %j", async (state, text) => {
    mocks.next = state;
    render(<SendTest id={ID} />);

    await send();

    expect(screen.getByRole("alert")).toHaveTextContent(text);
  });
});
