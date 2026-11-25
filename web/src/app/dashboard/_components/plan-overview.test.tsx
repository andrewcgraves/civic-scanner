import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/billing/actions", () => ({
  startCheckoutAction: vi.fn(),
  openBillingPortalAction: vi.fn(),
}));

import { PlanOverview, planStatus } from "./plan-overview";

function stubSession(response: () => Promise<Response>) {
  const fetchMock = vi.fn<typeof fetch>(() => response());
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function signedIn(plan: unknown) {
  return () =>
    Promise.resolve(
      Response.json({ signedIn: true, email: "a@example.com", plan }),
    );
}

async function renderOverview(billing = true, subscriptions = false) {
  const view = render(
    <PlanOverview billing={billing} subscriptions={subscriptions} />,
  );
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  return view;
}

describe("planStatus", () => {
  it("knows the plan of a signed-in user", () => {
    expect(
      planStatus({ signedIn: true, email: null, role: null, plan: "paid" }),
    ).toEqual({ kind: "known", plan: "paid" });
  });

  it("is unavailable without a session answer or a plan", () => {
    expect(planStatus(null)).toEqual({ kind: "unavailable" });
    expect(
      planStatus({ signedIn: true, email: null, role: null, plan: null }),
    ).toEqual({ kind: "unavailable" });
  });

  it("is signed out when the session has ended", () => {
    expect(planStatus({ signedIn: false })).toEqual({ kind: "signed-out" });
  });
});

describe("PlanOverview", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the plan as loading until the session arrives", () => {
    stubSession(() => new Promise<Response>(() => undefined));

    render(<PlanOverview billing />);

    expect(screen.getByText("Loading your plan…")).toBeVisible();
    expect(
      screen.getByRole("heading", { level: 2, name: "Your plan" }),
    ).toBeVisible();
    expect(
      screen.getByText(/Subscriptions will be part of the paid plan/),
    ).toBeVisible();
  });

  it("asks the server for the session without caching", async () => {
    const fetchMock = stubSession(signedIn("free"));

    await renderOverview();

    expect(fetchMock).toHaveBeenCalledWith("/auth/session", {
      cache: "no-store",
      headers: { accept: "application/json" },
    });
  });

  it("shows the free plan with the checkout button", async () => {
    stubSession(signedIn("free"));

    await renderOverview();

    expect(screen.getByText("Free plan")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Start the paid plan" }),
    ).toBeVisible();
    expect(
      screen.getByText(/Subscriptions will be part of the paid plan/),
    ).toBeVisible();
    expect(screen.queryByText("Loading your plan…")).toBeNull();
  });

  it("shows the paid plan and tells a paid user subscriptions come here", async () => {
    stubSession(signedIn("paid"));

    await renderOverview();

    expect(screen.getByText("Paid plan")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Open the billing portal" }),
    ).toBeVisible();
    expect(screen.getByText(/They will be set up here/)).toBeVisible();
  });

  it("links a paid user to their subscriptions when they are on", async () => {
    stubSession(signedIn("paid"));

    await renderOverview(true, true);

    expect(
      screen.getByRole("link", { name: "Manage your subscriptions" }),
    ).toHaveAttribute("href", "/dashboard/subscriptions");
    expect(screen.getByText(/get a webhook call/)).toBeVisible();
    expect(screen.getByText(/included in your plan\.$/)).toBeVisible();
    expect(screen.queryByText(/coming soon/)).toBeNull();
  });

  it("treats a granted user as paid, without the billing portal", async () => {
    stubSession(signedIn("granted"));

    await renderOverview(true, true);

    expect(screen.getByText("Paid plan")).toBeVisible();
    expect(screen.queryByText("Free plan")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
    expect(
      screen.getByRole("link", { name: "Manage your subscriptions" }),
    ).toHaveAttribute("href", "/dashboard/subscriptions");
  });

  it("points a free user at subscriptions when they are on", async () => {
    stubSession(signedIn("free"));

    await renderOverview(true, true);

    expect(
      screen.getByRole("link", { name: "See what subscriptions do" }),
    ).toHaveAttribute("href", "/dashboard/subscriptions");
    expect(
      screen.getByText("Webhook subscriptions are part of the paid plan."),
    ).toBeVisible();
    expect(screen.getByText(/adds webhook subscriptions\.$/)).toBeVisible();
  });

  it("passes billing being off to the plan card", async () => {
    stubSession(signedIn("free"));

    await renderOverview(false);

    expect(screen.getByText("Free plan")).toBeVisible();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it.each([
    ["a session without a plan", signedIn(null)],
    [
      "an error status",
      () => Promise.resolve(new Response(null, { status: 503 })),
    ],
    ["a network error", () => Promise.reject(new TypeError("Failed to fetch"))],
  ])(
    "explains that the plan cannot be loaded on %s",
    async (_name, response) => {
      stubSession(response);

      await renderOverview();

      expect(screen.getByRole("alert")).toHaveTextContent(
        "Your plan cannot be loaded right now",
      );
      expect(screen.queryByRole("button")).toBeNull();
    },
  );

  it("links to sign-in when the session has ended", async () => {
    stubSession(() => Promise.resolve(Response.json({ signedIn: false })));

    await renderOverview();

    expect(screen.getByText("You are signed out")).toBeVisible();
    expect(screen.getByRole("link", { name: "Sign in again" })).toHaveAttribute(
      "href",
      "/auth/sign-in?returnTo=%2Fdashboard",
    );
  });

  it("ignores a session that arrives after unmounting", async () => {
    let resolve: (response: Response) => void = () => undefined;
    stubSession(
      () =>
        new Promise<Response>((done) => {
          resolve = done;
        }),
    );
    const { unmount } = render(<PlanOverview billing />);
    unmount();

    await act(async () => {
      resolve(Response.json({ signedIn: false }));
      await new Promise((done) => setTimeout(done, 0));
    });

    expect(screen.queryByRole("link")).toBeNull();
  });
});
