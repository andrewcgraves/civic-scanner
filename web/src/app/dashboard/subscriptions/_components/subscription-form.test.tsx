import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { FormState } from "../state";

const mocks = vi.hoisted(() => ({
  next: undefined as FormState | undefined,
  pending: undefined as Promise<void> | undefined,
  calls: [] as { action: string; data: Record<string, unknown> }[],
}));

function fake(action: string) {
  return async (_previous: FormState, formData: FormData) => {
    mocks.calls.push({ action, data: Object.fromEntries(formData) });
    if (mocks.pending !== undefined) {
      await mocks.pending;
    }
    return mocks.next;
  };
}

vi.mock("../actions", () => ({
  createSubscription: fake("create"),
  updateSubscription: fake("update"),
}));

const { SubscriptionForm } = await import("./subscription-form");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";
const AGENCIES = [
  { slug: "la-metro", name: "LA Metro" },
  { slug: "octa", name: "Orange County Transportation Authority" },
];
const CREATE = { kind: "create", agencies: AGENCIES } as const;
const EDIT = {
  kind: "edit",
  id: ID,
  agency: "la-metro",
  agencyName: "LA Metro",
  url: "https://hooks.example.org/old",
  format: "slack",
} as const;
const SENT = {
  agency: "octa",
  url: "https://hooks.example.org/new",
  format: "discord",
};
const VALUES = { ...SENT, prompt: "" };
const PROMPT = "Anything about the Blue Line: closures and delays.";

function options(label: string) {
  return within(screen.getByLabelText(label))
    .queryAllByRole("option")
    .map((option) => option.textContent);
}

async function submit(name: string) {
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name }));
    await Promise.resolve();
  });
}

async function createWith(state: FormState, prompts = false) {
  mocks.next = state;
  render(<SubscriptionForm mode={CREATE} prompts={prompts} />);
  fireEvent.change(screen.getByLabelText("Agency"), {
    target: { value: VALUES.agency },
  });
  fireEvent.change(screen.getByLabelText("Endpoint URL"), {
    target: { value: VALUES.url },
  });
  fireEvent.change(screen.getByLabelText("Message format"), {
    target: { value: VALUES.format },
  });
  await submit("Add subscription");
}

describe("SubscriptionForm", () => {
  beforeEach(() => {
    mocks.next = undefined;
    mocks.pending = undefined;
    mocks.calls = [];
  });

  it("offers every agency, with labelled and described fields", () => {
    render(<SubscriptionForm mode={CREATE} />);

    const agency = screen.getByLabelText("Agency");
    expect(agency).toHaveValue("la-metro");
    expect(agency).toHaveAccessibleDescription(/posts a meeting/);
    expect(options("Agency")).toEqual([
      "LA Metro",
      "Orange County Transportation Authority",
    ]);
    const format = screen.getByLabelText("Message format");
    expect(format).toHaveValue("standard");
    expect(format).toBeRequired();
    expect(format).toHaveAccessibleDescription(/incoming webhook URL/);
    expect(options("Message format")).toEqual([
      "Standard JSON",
      "Discord",
      "Slack",
    ]);
    const url = screen.getByLabelText("Endpoint URL");
    expect(url).toBeRequired();
    expect(url).toHaveAttribute("inputMode", "url");
    expect(url).toHaveAttribute("maxLength", "2048");
    expect(url).toHaveAccessibleDescription(/https address on a public host/);
    expect(url).not.toHaveAttribute("aria-invalid");
    expect(screen.getByRole("link", { name: "Cancel" })).toHaveAttribute(
      "href",
      "/dashboard/subscriptions",
    );
    expect(
      screen.getByText(/shown once, right after you add it/),
    ).toBeVisible();
  });

  it("has no default agency when there are none", () => {
    render(<SubscriptionForm mode={{ kind: "create", agencies: [] }} />);

    expect(options("Agency")).toEqual([]);
  });

  it("sends the chosen agency and URL, then shows the secret once", async () => {
    await createWith({
      kind: "created",
      agency: "octa",
      url: VALUES.url,
      secret: "whsec_c2VjcmV0",
    });

    expect(mocks.calls).toEqual([{ action: "create", data: SENT }]);
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Subscription added");
    expect(status).toHaveTextContent(
      `Orange County Transportation Authority will send to ${VALUES.url}.`,
    );
    expect(screen.getByLabelText("Signing secret")).toHaveValue(
      "whsec_c2VjcmV0",
    );
    expect(
      screen.getByRole("link", { name: "Back to subscriptions" }),
    ).toHaveAttribute("href", "/dashboard/subscriptions");
    expect(screen.queryByLabelText("Endpoint URL")).toBeNull();
  });

  it("names an agency it does not know by its slug", async () => {
    await createWith({
      kind: "created",
      agency: "new-agency",
      url: VALUES.url,
      secret: "whsec_c2VjcmV0",
    });

    expect(screen.getByRole("status")).toHaveTextContent(
      "new-agency will send to",
    );
  });

  it("ties an API field error to the URL and lists it in the summary", async () => {
    await createWith({
      kind: "invalid",
      values: VALUES,
      errors: { url: "The URL's host must be a public internet address." },
    });

    const url = screen.getByLabelText("Endpoint URL");
    expect(url).toHaveAttribute("aria-invalid", "true");
    expect(url).toHaveAccessibleDescription(
      /public host.*The URL's host must be a public internet address\./,
    );
    expect(url).toHaveValue(VALUES.url);
    expect(screen.getByLabelText("Agency")).toHaveValue("octa");
    expect(screen.getByLabelText("Agency")).not.toHaveAttribute("aria-invalid");
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Please correct the form",
    );
    expect(screen.getByRole("link", { name: "Endpoint URL" })).toHaveAttribute(
      "href",
      "#url",
    );
  });

  it("ties an agency error to the agency", async () => {
    await createWith({
      kind: "invalid",
      values: VALUES,
      errors: { agency: "Choose an agency from the list." },
    });

    const agency = screen.getByLabelText("Agency");
    expect(agency).toHaveAttribute("aria-invalid", "true");
    expect(agency).toHaveAccessibleDescription(
      /posts a meeting.*Choose an agency from the list\./,
    );
  });

  it("ties a format error to the format and keeps the choice", async () => {
    await createWith({
      kind: "invalid",
      values: VALUES,
      errors: { format: "Choose a message format from the list." },
    });

    const format = screen.getByLabelText("Message format");
    expect(format).toHaveValue("discord");
    expect(format).toHaveAttribute("aria-invalid", "true");
    expect(format).toHaveAccessibleDescription(
      /incoming webhook URL.*Choose a message format from the list\./,
    );
    expect(
      screen.getByRole("link", { name: "Message format" }),
    ).toHaveAttribute("href", "#format");
  });

  it("shows a form-level message instead of a list", async () => {
    await createWith({
      kind: "invalid",
      values: VALUES,
      errors: {},
      message: "Check the form and try again.",
    });

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Check the form and try again.",
    );
    expect(screen.queryByRole("link", { name: "Endpoint URL" })).toBeNull();
  });

  it.each<[FormState["kind"], RegExp]>([
    ["duplicate", /already follow this agency at this URL/],
    ["limit", /at most 25 subscriptions/],
    ["upgrade", /need the paid plan/],
    ["gone", /not available to your account/],
    ["unavailable", /could not be saved right now/],
  ])("explains %s and keeps the values", async (kind, text) => {
    await createWith({ kind, values: VALUES } as FormState);

    expect(screen.getByRole("alert")).toHaveTextContent(text);
    expect(screen.getByLabelText("Endpoint URL")).toHaveValue(VALUES.url);
  });

  it("disables the button while saving", async () => {
    let release: () => void = () => undefined;
    mocks.pending = new Promise((done) => {
      release = done;
    });
    mocks.next = { kind: "unavailable", values: VALUES };
    render(<SubscriptionForm mode={CREATE} />);
    fireEvent.change(screen.getByLabelText("Endpoint URL"), {
      target: { value: VALUES.url },
    });

    await submit("Add subscription");

    const button = screen.getByRole("button", { name: "Saving…" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    await act(async () => {
      release();
      await mocks.pending;
    });
  });

  describe("directed prompt", () => {
    it("is not offered while directed prompts are off", () => {
      render(<SubscriptionForm mode={CREATE} />);

      expect(screen.queryByLabelText(/Directed prompt/)).toBeNull();
      expect(screen.queryByRole("button", { name: "Fare changes" })).toBeNull();
    });

    it("is an optional, described field with examples and a counter", () => {
      render(<SubscriptionForm mode={CREATE} prompts />);

      const prompt = screen.getByLabelText(/Directed prompt/);
      expect(prompt).toHaveValue("");
      expect(prompt).not.toBeRequired();
      expect(prompt).toHaveAccessibleDescription(
        /nothing is sent\. Leave it empty to get every event\. 0 \/ 500 characters/,
      );
      expect(
        screen.getAllByRole("button", { name: /Fare|rail|Contracts/ }),
      ).toHaveLength(3);
    });

    it("sends the prompt with the rest of the form", async () => {
      mocks.next = { kind: "unavailable", values: VALUES };
      render(<SubscriptionForm mode={CREATE} prompts />);
      fireEvent.change(screen.getByLabelText("Endpoint URL"), {
        target: { value: VALUES.url },
      });
      fireEvent.change(screen.getByLabelText(/Directed prompt/), {
        target: { value: PROMPT },
      });

      await submit("Add subscription");

      expect(mocks.calls).toEqual([
        {
          action: "create",
          data: {
            agency: "la-metro",
            url: VALUES.url,
            format: "standard",
            prompt: PROMPT,
          },
        },
      ]);
    });

    it("ties a prompt error to the prompt and keeps the text", async () => {
      await createWith(
        {
          kind: "invalid",
          values: { ...VALUES, prompt: PROMPT },
          errors: { prompt: "Directed prompts are not available." },
        },
        true,
      );

      const prompt = screen.getByLabelText(/Directed prompt/);
      expect(prompt).toHaveValue(PROMPT);
      expect(prompt).toHaveAttribute("aria-invalid", "true");
      expect(prompt).toHaveAccessibleDescription(
        /characters Directed prompts are not available\./,
      );
      expect(
        screen.getByRole("link", { name: "Directed prompt" }),
      ).toHaveAttribute("href", "#prompt");
    });

    it("starts from the saved prompt when editing, and can be cleared", async () => {
      mocks.next = { kind: "unavailable", values: VALUES };
      render(<SubscriptionForm mode={{ ...EDIT, prompt: PROMPT }} prompts />);

      const prompt = screen.getByLabelText(/Directed prompt/);
      expect(prompt).toHaveValue(PROMPT);
      expect(prompt).toHaveAccessibleDescription(
        /Clear it to get every event again\./,
      );
      expect(screen.queryByRole("button", { name: "Fare changes" })).toBeNull();
      fireEvent.change(prompt, { target: { value: "  " } });

      await submit("Save changes");

      expect(mocks.calls[0]?.data).toMatchObject({ prompt: "  " });
    });
  });

  describe("editing", () => {
    it("fixes the agency and changes the URL and the format", async () => {
      mocks.next = { kind: "unavailable", values: VALUES };
      render(<SubscriptionForm mode={EDIT} />);

      expect(screen.queryByLabelText("Agency")).toBeNull();
      expect(screen.getByText("LA Metro")).toBeVisible();
      expect(screen.getByText(/agency cannot be changed/)).toBeVisible();
      expect(screen.getByText(/keeps the same signing secret/)).toBeVisible();
      const url = screen.getByLabelText("Endpoint URL");
      expect(url).toHaveValue(EDIT.url);
      fireEvent.change(url, { target: { value: VALUES.url } });
      const format = screen.getByLabelText("Message format");
      expect(format).toHaveValue("slack");
      fireEvent.change(format, { target: { value: "discord" } });

      await submit("Save changes");

      expect(mocks.calls).toEqual([
        {
          action: "update",
          data: {
            id: ID,
            agency: "la-metro",
            url: VALUES.url,
            format: "discord",
          },
        },
      ]);
    });

    it("says when the subscription no longer exists", async () => {
      mocks.next = { kind: "gone", values: VALUES };
      render(<SubscriptionForm mode={EDIT} />);

      await submit("Save changes");

      expect(screen.getByRole("alert")).toHaveTextContent(
        "This subscription no longer exists.",
      );
      expect(
        screen.getByRole("link", { name: "Back to subscriptions" }),
      ).toBeVisible();
    });

    it("names the agency it was editing by slug if the secret ever appears", async () => {
      mocks.next = {
        kind: "created",
        agency: "la-metro",
        url: VALUES.url,
        secret: "whsec_eA==",
      };
      render(<SubscriptionForm mode={EDIT} />);

      await submit("Save changes");

      expect(screen.getByRole("status")).toHaveTextContent(
        "la-metro will send to",
      );
    });
  });
});
