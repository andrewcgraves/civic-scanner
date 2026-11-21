import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { ActionState } from "../state";

const mocks = vi.hoisted(() => {
  const idle = (): ActionState => ({ kind: "idle" });
  return {
    next: idle(),
    calls: [] as { action: string; data: Record<string, unknown> }[],
  };
});

function fake(action: string) {
  return (_previous: ActionState, formData: FormData) => {
    mocks.calls.push({ action, data: Object.fromEntries(formData) });
    return Promise.resolve(mocks.next);
  };
}

vi.mock("../actions", () => ({
  pauseSubscription: fake("pause"),
  resumeSubscription: fake("resume"),
  rotateSecret: fake("rotate"),
  deleteSubscription: fake("delete"),
}));

const { SubscriptionActions } = await import("./subscription-actions");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function showModal(
    this: HTMLDialogElement,
  ) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    this.open = false;
  };
});

function renderActions(paused = false, statusButton?: boolean) {
  return render(
    <SubscriptionActions
      id={ID}
      agency="la-metro"
      agencyName="LA Metro"
      url="https://hooks.example.org/civic"
      paused={paused}
      statusButton={statusButton}
    />,
  );
}

async function click(name: string | RegExp) {
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name, hidden: true }));
    await Promise.resolve();
  });
}

describe("SubscriptionActions", () => {
  beforeEach(() => {
    mocks.next = { kind: "idle" };
    mocks.calls = [];
  });

  it("links to the edit page", () => {
    renderActions();

    expect(screen.getByRole("link", { name: "Edit LA Metro" })).toHaveAttribute(
      "href",
      `/dashboard/subscriptions/${ID}/edit`,
    );
  });

  it("links to the subscription's deliveries and tests", () => {
    renderActions();

    expect(
      screen.getByRole("link", { name: "Deliveries and tests for LA Metro" }),
    ).toHaveAttribute("href", `/dashboard/subscriptions/${ID}`);
  });

  it("leaves out the status button when asked", () => {
    renderActions(true, false);

    expect(
      screen.queryByRole("button", { name: "Resume LA Metro" }),
    ).toBeNull();
    expect(
      screen.getByRole("button", { name: "Delete LA Metro" }),
    ).toBeVisible();
  });

  it("pauses an active subscription at once", async () => {
    renderActions();

    await click("Pause LA Metro");

    expect(mocks.calls).toEqual([{ action: "pause", data: { id: ID } }]);
  });

  it("resumes a paused subscription", async () => {
    renderActions(true);

    await click("Resume LA Metro");

    expect(mocks.calls).toEqual([{ action: "resume", data: { id: ID } }]);
  });

  it("deletes only after confirming", async () => {
    renderActions();
    fireEvent.click(screen.getByRole("button", { name: "Delete LA Metro" }));
    const dialog = screen.getByRole("dialog", {
      name: "Delete the LA Metro subscription?",
    });
    expect(dialog).toHaveTextContent("https://hooks.example.org/civic");
    expect(mocks.calls).toEqual([]);

    await click("Delete subscription");

    expect(mocks.calls).toEqual([
      { action: "delete", data: { id: ID, agency: "la-metro" } },
    ]);
  });

  it("rotates after confirming and shows the new secret until hidden", async () => {
    mocks.next = { kind: "rotated", secret: "whsec_bmV3" };
    renderActions();
    fireEvent.click(
      screen.getByRole("button", { name: "Rotate the secret for LA Metro" }),
    );

    await click(/^Rotate secret$/);

    expect(mocks.calls).toEqual([
      { action: "rotate", data: { id: ID, agency: "la-metro" } },
    ]);
    const panel = screen.getByRole("status");
    expect(panel).toHaveTextContent("New secret for LA Metro");
    expect(within(panel).getByLabelText("Signing secret")).toHaveValue(
      "whsec_bmV3",
    );
    const dialogs = [...document.querySelectorAll("dialog")];
    expect(dialogs.map((dialog) => dialog.open)).toEqual([false, false]);

    fireEvent.click(
      screen.getByRole("button", { name: "Done, hide the secret" }),
    );

    expect(screen.queryByRole("status")).toBeNull();
  });

  it.each<[ActionState, RegExp]>([
    [{ kind: "gone" }, /no longer exists/],
    [{ kind: "upgrade" }, /need the paid plan/],
    [{ kind: "unavailable" }, /could not be done right now/],
  ])("explains %j", async (state, text) => {
    mocks.next = state;
    renderActions();

    await click("Pause LA Metro");

    expect(screen.getByRole("alert")).toHaveTextContent(text);
  });
});
