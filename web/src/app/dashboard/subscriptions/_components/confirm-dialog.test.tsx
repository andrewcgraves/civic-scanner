import { fireEvent, render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, it, vi } from "vitest";

import { ConfirmDialog } from "./confirm-dialog";

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

function renderDialog({
  pending = false,
  danger = false,
  closeWhen = 0,
  action = vi.fn(),
}: {
  pending?: boolean;
  danger?: boolean;
  closeWhen?: unknown;
  action?: (formData: FormData) => void;
} = {}) {
  return render(
    <ConfirmDialog
      triggerLabel="Delete"
      triggerAccessibleName="Delete LA Metro"
      triggerClassName="trigger"
      title="Delete the LA Metro subscription?"
      confirmLabel="Delete subscription"
      pendingLabel="Deleting…"
      cancelLabel="Keep it"
      danger={danger}
      action={action}
      pending={pending}
      closeWhen={closeWhen}
      fields={{ id: "abc", agency: "la-metro" }}
    >
      <p>This cannot be undone.</p>
    </ConfirmDialog>,
  );
}

function dialog(): HTMLDialogElement {
  return screen.getByRole<HTMLDialogElement>("dialog", { hidden: true });
}

describe("ConfirmDialog", () => {
  it("opens from its trigger and closes on cancel", () => {
    renderDialog();
    expect(dialog().open).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Delete LA Metro" }));

    expect(dialog().open).toBe(true);
    expect(dialog()).toHaveAccessibleName("Delete the LA Metro subscription?");
    expect(dialog()).toHaveTextContent("This cannot be undone.");

    fireEvent.click(screen.getByRole("button", { name: "Keep it" }));

    expect(dialog().open).toBe(false);
  });

  it("submits its hidden fields to the action", () => {
    const action = vi.fn();
    renderDialog({ action });
    fireEvent.click(screen.getByRole("button", { name: "Delete LA Metro" }));

    fireEvent.click(
      screen.getByRole("button", { name: "Delete subscription" }),
    );

    expect(action).toHaveBeenCalledTimes(1);
    const data = action.mock.calls[0]?.[0] as FormData;
    expect(Object.fromEntries(data)).toEqual({ id: "abc", agency: "la-metro" });
  });

  it("styles the confirm button as destructive only when asked", () => {
    const { unmount } = renderDialog({ danger: true });
    expect(
      screen.getByRole("button", { name: "Delete subscription", hidden: true })
        .className,
    ).toContain("bg-error-ink");
    unmount();

    renderDialog();
    expect(
      screen.getByRole("button", { name: "Delete subscription", hidden: true })
        .className,
    ).not.toContain("bg-error-ink");
  });

  it("shows progress while pending", () => {
    renderDialog({ pending: true });

    const confirm = screen.getByRole("button", {
      name: "Deleting…",
      hidden: true,
    });
    expect(confirm).toBeDisabled();
    expect(confirm).toHaveAttribute("aria-busy", "true");
  });

  it("closes when the action's state changes", () => {
    const { rerender } = renderDialog({ closeWhen: 1 });
    fireEvent.click(screen.getByRole("button", { name: "Delete LA Metro" }));
    expect(dialog().open).toBe(true);

    rerender(
      <ConfirmDialog
        triggerLabel="Delete"
        triggerAccessibleName="Delete LA Metro"
        triggerClassName="trigger"
        title="Delete the LA Metro subscription?"
        confirmLabel="Delete subscription"
        pendingLabel="Deleting…"
        cancelLabel="Keep it"
        action={vi.fn()}
        pending={false}
        closeWhen={2}
        fields={{}}
      >
        <p>Changed.</p>
      </ConfirmDialog>,
    );

    expect(dialog().open).toBe(false);
  });
});
