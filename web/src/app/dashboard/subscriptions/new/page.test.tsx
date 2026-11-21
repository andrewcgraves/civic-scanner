import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AgencyChoices } from "../server";

const mocks = vi.hoisted(() => {
  const unloaded = (): AgencyChoices => ({ ok: false, names: new Map() });
  return { required: [] as string[], choices: unloaded() };
});

vi.mock("../server", () => ({
  requireSubscriptions: () => {
    mocks.required.push("subscriptions");
    return Promise.resolve();
  },
  loadAgencyChoices: () => Promise.resolve(mocks.choices),
}));
vi.mock("../_components/subscription-form", () => ({
  SubscriptionForm: ({
    mode,
  }: {
    mode: { kind: string; agencies: { slug: string; name: string }[] };
  }) => <p>{`${mode.kind} form: ${JSON.stringify(mode.agencies)}`}</p>,
}));

const { default: NewSubscriptionPage, metadata } = await import("./page");

describe("NewSubscriptionPage", () => {
  beforeEach(() => {
    mocks.required = [];
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

  it("explains when the agencies cannot be loaded", async () => {
    mocks.choices = { ok: false, names: new Map() };

    render(await NewSubscriptionPage());

    expect(screen.getByRole("alert")).toHaveTextContent(
      "The list of agencies is unavailable right now",
    );
    expect(screen.queryByText(/form/)).toBeNull();
  });
});
