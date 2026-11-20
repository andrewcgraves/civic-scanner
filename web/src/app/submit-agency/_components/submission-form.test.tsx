import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { SubmissionState } from "../state";

const mocks = vi.hoisted(() => ({
  next: undefined as SubmissionState | undefined,
  pending: undefined as Promise<void> | undefined,
  received: [] as FormData[],
}));

vi.mock("../actions", () => ({
  submitAgency: async (_previous: SubmissionState, formData: FormData) => {
    mocks.received.push(formData);
    if (mocks.pending !== undefined) {
      await mocks.pending;
    }
    return mocks.next;
  },
}));

const { SubmissionForm } = await import("./submission-form");

const VALUES = { name: "Big Bear Transit", website: "bigbeartransit.org" };

async function submitWith(state: SubmissionState) {
  mocks.next = state;
  render(<SubmissionForm />);
  fireEvent.change(screen.getByLabelText("Agency name"), {
    target: { value: VALUES.name },
  });
  fireEvent.change(screen.getByLabelText("Website"), {
    target: { value: VALUES.website },
  });
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", { name: "Suggest this agency" }),
    );
    await Promise.resolve();
  });
}

describe("SubmissionForm", () => {
  beforeEach(() => {
    mocks.next = undefined;
    mocks.pending = undefined;
    mocks.received = [];
  });

  it("renders labelled, required fields with hints", () => {
    render(<SubmissionForm />);

    const name = screen.getByLabelText("Agency name");
    const website = screen.getByLabelText("Website");
    expect(name).toBeRequired();
    expect(name).toHaveAttribute("name", "name");
    expect(name).toHaveAttribute("maxLength", "200");
    expect(name).toHaveAccessibleDescription(/official name/);
    expect(name).not.toHaveAttribute("aria-invalid");
    expect(website).toBeRequired();
    expect(website).toHaveAttribute("inputMode", "url");
    expect(website).toHaveAttribute("maxLength", "2048");
    expect(website).toHaveAccessibleDescription(/own website/);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("sends the typed values and shows the stored submission", async () => {
    await submitWith({
      kind: "submitted",
      name: "Big Bear Transit",
      website: "https://bigbeartransit.org",
    });

    expect(mocks.received).toHaveLength(1);
    expect(Object.fromEntries(mocks.received[0] ?? [])).toEqual(VALUES);
    expect(
      screen.getByText("Thanks, your suggestion was sent"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Big Bear Transit \(https:\/\/bigbeartransit\.org\)/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Suggest another agency" }),
    ).toHaveAttribute("href", "/submit-agency");
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("announces the success and moves focus to it", async () => {
    await submitWith({
      kind: "submitted",
      name: "Big Bear Transit",
      website: "https://bigbeartransit.org",
    });

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Thanks, your suggestion was sent");
    expect(status).toHaveFocus();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("ties each field error to its field and lists it in the summary", async () => {
    await submitWith({
      kind: "invalid",
      values: VALUES,
      errors: { website: "Website must use a host name, not an IP address." },
    });

    const website = screen.getByLabelText("Website");
    expect(website).toHaveAttribute("aria-invalid", "true");
    expect(website).toHaveAccessibleDescription(
      /own website.*Website must use a host name, not an IP address\./,
    );
    expect(website).toHaveValue(VALUES.website);
    expect(screen.getByLabelText("Agency name")).not.toHaveAttribute(
      "aria-invalid",
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Please correct the form");
    expect(screen.getByRole("link", { name: "Website" })).toHaveAttribute(
      "href",
      "#website",
    );
  });

  it("shows a form-level message when the fields could not be read", async () => {
    await submitWith({
      kind: "invalid",
      values: VALUES,
      errors: {},
      message: "Check the name and website and try again.",
    });

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Check the name and website and try again.",
    );
  });

  it.each<[SubmissionState["kind"], RegExp]>([
    ["on-file", /already on file/],
    ["limit", /reached the limit/],
    ["closed", /not open right now/],
    ["unavailable", /could not be sent right now/],
  ])("explains %s and keeps the values", async (kind, text) => {
    await submitWith({ kind, values: VALUES } as SubmissionState);

    expect(screen.getByRole("alert")).toHaveTextContent(text);
    expect(screen.getByLabelText("Agency name")).toHaveValue(VALUES.name);
  });

  it("disables the button while sending", async () => {
    let release = () => undefined as void;
    mocks.pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    mocks.next = { kind: "limit", values: VALUES };
    render(<SubmissionForm />);
    fireEvent.change(screen.getByLabelText("Agency name"), {
      target: { value: VALUES.name },
    });
    fireEvent.change(screen.getByLabelText("Website"), {
      target: { value: VALUES.website },
    });

    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Suggest this agency" }),
      );
      await Promise.resolve();
    });
    expect(screen.getByRole("button", { name: "Sending…" })).toBeDisabled();

    await act(async () => {
      release();
      await mocks.pending;
    });
    expect(
      screen.getByRole("button", { name: "Suggest this agency" }),
    ).toBeEnabled();
  });
});
