import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AgencyChoices } from "../server";

const mocks = vi.hoisted(() => {
  const unloaded = (): AgencyChoices => ({ ok: false, names: new Map() });
  return { required: [] as string[], choices: unloaded(), prompts: false };
});

vi.mock("../server", () => ({
  requireSubscriptions: () => {
    mocks.required.push("subscriptions");
    return Promise.resolve();
  },
  loadAgencyChoices: () => Promise.resolve(mocks.choices),
  isDirectedPromptsEnabled: () => Promise.resolve(mocks.prompts),
}));
vi.mock("../_components/subscription-form", () => ({
  SubscriptionForm: ({
    mode,
    prompts,
  }: {
    mode: { kind: string; agencies: { slug: string; name: string }[] };
    prompts: boolean;
  }) => (
    <p>{`${mode.kind} form${prompts ? " with a prompt" : ""}: ${JSON.stringify(mode.agencies)}`}</p>
  ),
}));

const { default: NewSubscriptionPage, metadata } = await import("./page");

describe("NewSubscriptionPage", () => {
  beforeEach(() => {
    mocks.required = [];
    mocks.prompts = false;
  });

  it("is titled and kept out of search results", () => {
    expect(metadata).toEqual({
      title: "Add a subscription",
      robots: { index: false },
    });
  });

  it("offers the live agencies by slug and name", async () => {
    mocks.choices = {
      ok: true,
      agencies: [
        {
          id: "a",
          slug: "la-metro",
          name: "LA Metro",
          website: "https://www.metro.net",
        },
      ],
      names: new Map(),
    };

    render(await NewSubscriptionPage());

    expect(mocks.required).toEqual(["subscriptions"]);
    expect(
      screen.getByRole("heading", { level: 1, name: "Add a subscription" }),
    ).toBeVisible();
    expect(screen.getByRole("link", { name: "Subscriptions" })).toHaveAttribute(
      "href",
      "/dashboard/subscriptions",
    );
    expect(
      screen.getByText('create form: [{"slug":"la-metro","name":"LA Metro"}]'),
    ).toBeVisible();
  });

  it("offers the prompt only while directed prompts are on", async () => {
    mocks.prompts = true;
    mocks.choices = {
      ok: true,
      agencies: [
        {
          id: "a",
          slug: "la-metro",
          name: "LA Metro",
          website: "https://www.metro.net",
        },
      ],
      names: new Map(),
    };

    render(await NewSubscriptionPage());

    expect(screen.getByText(/^create form with a prompt: /)).toBeVisible();
  });

  it("explains when the agencies cannot be loaded", async () => {
    mocks.choices = { ok: false, names: new Map() };

    render(await NewSubscriptionPage());

    expect(screen.getByRole("alert")).toHaveTextContent(
      "The list of agencies is unavailable right now",
    );
    expect(screen.queryByText(/form/)).toBeNull();
  });
});
