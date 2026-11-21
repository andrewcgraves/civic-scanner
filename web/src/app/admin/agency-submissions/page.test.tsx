import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

const mocks = vi.hoisted(() => {
  const failed = (): UserApiResult<unknown> => ({ status: "failed" });
  return {
    signIn: true,
    hasCookie: true,
    result: failed(),
    calls: [] as string[],
    notFound: vi.fn(() => {
      throw new Error("NEXT_NOT_FOUND");
    }),
    redirect: vi.fn((path: string) => {
      throw new Error(`NEXT_REDIRECT ${path}`);
    }),
  };
});

vi.mock("next/server", () => ({ connection: () => Promise.resolve() }));
vi.mock("next/navigation", () => ({
  notFound: mocks.notFound,
  redirect: mocks.redirect,
}));
vi.mock("next/headers", () => ({
  cookies: () =>
    Promise.resolve({
      has: (name: string) => mocks.hasCookie && name === "wos-session",
    }),
}));
vi.mock("@/lib/auth/server", () => ({
  isSignInEnabled: () => Promise.resolve(mocks.signIn),
}));
vi.mock("@/lib/auth/api", () => ({
  readApiAsUser: async (
    call: (client: UserApiClient) => Promise<unknown>,
  ): Promise<UserApiResult<unknown>> => {
    const client = {
      GET: (path: string) => {
        mocks.calls.push(path);
        return Promise.resolve({ response: new Response() });
      },
    } as unknown as UserApiClient;
    await call(client);
    return mocks.result;
  },
}));
vi.mock("./_components/review-item", () => ({
  ReviewItem: ({ submission }: { submission: { name: string } }) => (
    <article aria-label={submission.name} />
  ),
}));

const { default: ReviewAgenciesPage, metadata } = await import("./page");

function page(params: Record<string, string | string[] | undefined> = {}) {
  return ReviewAgenciesPage({ searchParams: Promise.resolve(params) });
}

const SUBMISSIONS = [
  { id: "a", name: "First Transit" },
  { id: "b", name: "Second Transit" },
];

describe("ReviewAgenciesPage", () => {
  beforeEach(() => {
    mocks.signIn = true;
    mocks.hasCookie = true;
    mocks.result = { status: "ok", data: { submissions: SUBMISSIONS } };
    mocks.calls = [];
    mocks.notFound.mockClear();
    mocks.redirect.mockClear();
  });

  it("is titled and kept out of search results", () => {
    expect(metadata.title).toBe("Review agencies");
    expect(metadata.robots).toEqual({ index: false });
  });

  it("lists the pending agencies in the API's order", async () => {
    render(await page());

    expect(
      screen.getByRole("heading", { level: 1, name: "Review agencies" }),
    ).toBeVisible();
    expect(mocks.calls).toEqual(["/admin/agency-submissions"]);
    expect(
      screen.getAllByRole("article").map((a) => a.getAttribute("aria-label")),
    ).toEqual(["First Transit", "Second Transit"]);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("says when nothing is waiting", async () => {
    mocks.result = { status: "ok", data: { submissions: [] } };
    render(await page());

    expect(screen.getByText("Nothing to review")).toBeVisible();
  });

  it("confirms an approval with a link to the live agency", async () => {
    render(await page({ approved: "big-bear" }));

    expect(screen.getByRole("status")).toHaveTextContent("The agency is live");
    expect(
      screen.getByRole("link", { name: "/agencies/big-bear" }),
    ).toHaveAttribute("href", "/agencies/big-bear");
  });

  it("confirms a rejection", async () => {
    render(await page({ rejected: "1" }));

    expect(screen.getByRole("status")).toHaveTextContent(
      "The agency was rejected",
    );
  });

  it("ignores an approval slug that is not a slug", async () => {
    render(await page({ approved: "javascript:alert(1)" }));

    expect(screen.queryByRole("status")).toBeNull();
  });

  it("is a 404 with sign-in off, without calling the API", async () => {
    mocks.signIn = false;
    await expect(page()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(mocks.calls).toEqual([]);
  });

  it("sends a visitor without a session to sign in", async () => {
    mocks.hasCookie = false;
    await expect(page()).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fadmin%2Fagency-submissions",
    );
    expect(mocks.calls).toEqual([]);
  });

  it("sends a session the API refuses to sign in", async () => {
    mocks.result = { status: "signed-out" };
    await expect(page()).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fadmin%2Fagency-submissions",
    );
  });

  it.each<UserApiResult<unknown>>([
    { status: "forbidden" },
    { status: "not-found" },
  ])("is a 404 for someone who is not an admin ($status)", async (result) => {
    mocks.result = result;
    await expect(page()).rejects.toThrow("NEXT_NOT_FOUND");
  });

  it.each<UserApiResult<unknown>>([
    { status: "unavailable" },
    { status: "failed" },
  ])(
    "keeps the page with a notice when the list is $status",
    async (result) => {
      mocks.result = result;
      render(await page({ rejected: "1" }));

      expect(screen.getByRole("alert")).toHaveTextContent(
        "Suggested agencies are unavailable right now",
      );
      expect(screen.queryByRole("article")).toBeNull();
    },
  );
});
