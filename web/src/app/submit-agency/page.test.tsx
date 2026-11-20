import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  flagsOn: new Set<string>(),
  signIn: true,
  hasCookie: true,
  user: null as { id: string } | null,
  requestHeaders: new Headers(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
  redirect: vi.fn((path: string) => {
    throw new Error(`NEXT_REDIRECT ${path}`);
  }),
  withAuth: vi.fn(),
}));

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
  headers: () => Promise.resolve(mocks.requestHeaders),
}));
vi.mock("@workos-inc/authkit-nextjs", () => ({ withAuth: mocks.withAuth }));
vi.mock("@/lib/flags/server", () => ({
  requireFlag: (flag: string) => {
    if (!mocks.flagsOn.has(flag)) {
      mocks.notFound();
    }
    return Promise.resolve();
  },
}));
vi.mock("@/lib/auth/server", () => ({
  isSignInEnabled: () => Promise.resolve(mocks.signIn),
}));
vi.mock("./_components/submission-form", () => ({
  SubmissionForm: () => <form aria-label="Suggestion form" />,
}));

const { default: SubmitAgencyPage, metadata } = await import("./page");

describe("SubmitAgencyPage", () => {
  beforeEach(() => {
    mocks.flagsOn = new Set(["agency-submissions"]);
    mocks.signIn = true;
    mocks.hasCookie = true;
    mocks.requestHeaders = new Headers();
    mocks.withAuth.mockReset();
    mocks.withAuth.mockResolvedValue({ user: { id: "user_1" } });
    mocks.notFound.mockClear();
    mocks.redirect.mockClear();
  });

  it("is titled and kept out of search results", () => {
    expect(metadata.title).toBe("Suggest an agency");
    expect(metadata.robots).toEqual({ index: false });
  });

  it("shows the form to a signed-in visitor", async () => {
    render(await SubmitAgencyPage());

    expect(
      screen.getByRole("heading", { level: 1, name: "Suggest an agency" }),
    ).toBeVisible();
    expect(screen.getByText(/reviewed before an agency appears/)).toBeVisible();
    expect(
      screen.getByRole("form", { name: "Suggestion form" }),
    ).toBeInTheDocument();
  });

  it("is a 404 with the flag off", async () => {
    mocks.flagsOn = new Set();

    await expect(SubmitAgencyPage()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(mocks.withAuth).not.toHaveBeenCalled();
  });

  it("is a 404 when sign-in is off", async () => {
    mocks.signIn = false;

    await expect(SubmitAgencyPage()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(mocks.withAuth).not.toHaveBeenCalled();
  });

  it("sends a visitor without a session to sign in and back", async () => {
    mocks.hasCookie = false;

    await expect(SubmitAgencyPage()).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fsubmit-agency",
    );
    expect(mocks.withAuth).not.toHaveBeenCalled();
  });

  it("sends a visitor whose session ended to sign in", async () => {
    mocks.withAuth.mockResolvedValue({ user: null });

    await expect(SubmitAgencyPage()).rejects.toThrow("NEXT_REDIRECT");
  });

  it("says so when the session cannot be checked", async () => {
    mocks.withAuth.mockResolvedValue({ user: null });
    mocks.requestHeaders = new Headers({
      "x-civic-session-state": "unverified",
    });

    render(await SubmitAgencyPage());

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Your sign-in cannot be checked right now.",
    );
    expect(screen.queryByRole("form")).toBeNull();
  });
});
