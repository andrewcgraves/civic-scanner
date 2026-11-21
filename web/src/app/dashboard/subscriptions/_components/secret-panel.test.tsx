import Link from "next/link";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SecretPanel } from "./secret-panel";

const SECRET = "whsec_c2VjcmV0";

function renderPanel() {
  return render(
    <SecretPanel
      title="Subscription added"
      secret={SECRET}
      footer={
        <Link href="/dashboard/subscriptions">Back to subscriptions</Link>
      }
    >
      LA Metro will send to you.
    </SecretPanel>,
  );
}

function stubClipboard(writeText: (text: string) => Promise<void>) {
  const spy = vi.fn(writeText);
  vi.stubGlobal("navigator", { clipboard: { writeText: spy } });
  return spy;
}

describe("SecretPanel", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the secret once, read-only, with its warning, and takes focus", () => {
    renderPanel();

    const status = screen.getByRole("status");
    expect(status).toHaveFocus();
    expect(status).toHaveTextContent("Subscription added");
    expect(status).toHaveTextContent("LA Metro will send to you.");
    const input = screen.getByLabelText("Signing secret");
    expect(input).toHaveValue(SECRET);
    expect(input).toHaveAttribute("readonly");
    expect(input).toHaveAccessibleDescription(
      /came from Civic Scanner.*only time the secret is shown/,
    );
    expect(
      screen.getByRole("link", { name: "Back to subscriptions" }),
    ).toBeVisible();
  });

  it("selects the secret when it gets focus", () => {
    renderPanel();
    const input = screen.getByLabelText<HTMLInputElement>("Signing secret");

    fireEvent.focus(input);

    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe(SECRET.length);
  });

  it("copies the secret to the clipboard", async () => {
    const writeText = stubClipboard(() => Promise.resolve());
    renderPanel();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy secret" }));
      await Promise.resolve();
    });

    expect(writeText).toHaveBeenCalledWith(SECRET);
    expect(screen.getByRole("button", { name: "Copied" })).toBeVisible();
    expect(screen.getByText("The secret is on your clipboard.")).toBeVisible();
  });

  it("asks the user to copy it by hand when the clipboard refuses", async () => {
    stubClipboard(() => Promise.reject(new Error("denied")));
    renderPanel();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy secret" }));
      await Promise.resolve();
    });

    expect(screen.getByRole("button", { name: "Copy secret" })).toBeVisible();
    expect(
      screen.getByText(/could not be copied\. Select the secret/),
    ).toBeVisible();
  });
});
