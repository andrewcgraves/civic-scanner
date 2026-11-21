import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ActionState } from "../state";

const mocks = vi.hoisted(() => {
  const idle = (): ActionState => ({ kind: "idle" });
  return { next: idle(), calls: [] as Record<string, unknown>[] };
});

vi.mock("../actions", () => ({
  resumeSubscription: (_previous: ActionState, formData: FormData) => {
    mocks.calls.push(Object.fromEntries(formData));
    return Promise.resolve(mocks.next);
  },
}));

const { PausedNotice } = await import("./paused-notice");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";
const PAUSED_AT = "2026-11-18T17:40:00Z";

async function resume(name: string) {
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name }));
    await Promise.resolve();
  });
}

describe("PausedNotice", () => {
  beforeEach(() => {
    mocks.next = { kind: "idle" };
    mocks.calls = [];
  });

  it("explains a pause after failed deliveries and resumes back to the page", async () => {
    render(
      <PausedNotice
        id={ID}
        agencyName="LA Metro"
        reason="failed_deliveries"
        pausedAt={PAUSED_AT}
      />,
    );

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(
      "Paused after 5 events in a row failed to deliver",
    );
    expect(alert).toHaveTextContent(
      /On Wed, Nov 18, 2026, 9:40.AM PST, Civic Scanner stopped sending to this URL/,
    );
    expect(alert).toHaveTextContent(
      "Events while it is paused are not sent later.",
    );
    expect(
      screen.getByRole("button", { name: "Resume LA Metro" }),
    ).toHaveTextContent("Resume subscription");

    await resume("Resume LA Metro");

    expect(mocks.calls).toEqual([{ id: ID, returnTo: "subscription" }]);
  });

  it("keeps a short version on the list, resuming back to the list", async () => {
    render(
      <PausedNotice
        id={ID}
        agencyName="LA Metro"
        reason="failed_deliveries"
        pausedAt={PAUSED_AT}
        compact
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      /Since Wed, Nov 18, 2026, 9:40.AM PST\. Events while it is paused/,
    );
    expect(
      screen.getByRole("button", { name: "Resume LA Metro" }),
    ).toHaveTextContent(/^Resume$/);

    await resume("Resume LA Metro");

    expect(mocks.calls).toEqual([{ id: ID }]);
  });

  it.each([false, true])(
    "copes without a pause time (compact: %s)",
    (compact) => {
      render(
        <PausedNotice
          id={ID}
          agencyName="LA Metro"
          reason="failed_deliveries"
          pausedAt={null}
          compact={compact}
        />,
      );

      const alert = screen.getByRole("alert");
      expect(alert).not.toHaveTextContent("Since");
      expect(alert).not.toHaveTextContent(/On .*Civic Scanner/);
      expect(alert).toHaveTextContent(
        "Events while it is paused are not sent later.",
      );
    },
  );

  it("says when the owner paused it", () => {
    const { container } = render(
      <PausedNotice
        id={ID}
        agencyName="LA Metro"
        reason="owner"
        pausedAt={PAUSED_AT}
      />,
    );

    expect(screen.queryByRole("alert")).toBeNull();
    expect(container).toHaveTextContent(
      /You paused this subscription on Wed, Nov 18, 2026, 9:40.AM PST/,
    );
    expect(
      screen.getByText(/No webhooks are sent to it until you resume it/),
    ).toBeVisible();
  });

  it("treats an unknown reason as the owner's pause", () => {
    render(
      <PausedNotice
        id={ID}
        agencyName="LA Metro"
        reason={null}
        pausedAt={null}
      />,
    );

    expect(screen.getByText("You paused this subscription")).toBeVisible();
  });

  it("explains a failed resume", async () => {
    mocks.next = { kind: "unavailable" };
    render(
      <PausedNotice
        id={ID}
        agencyName="LA Metro"
        reason="owner"
        pausedAt={null}
      />,
    );

    await resume("Resume LA Metro");

    expect(screen.getByRole("alert")).toHaveTextContent(
      /could not be done right now/,
    );
  });
});
