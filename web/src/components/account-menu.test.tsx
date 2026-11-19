import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  pathname: "/agencies",
  query: "",
  suspend: false,
  signOutAction: vi.fn(),
}));

const never = new Promise<never>(() => undefined);

vi.mock("next/navigation", () => ({
  usePathname: () => mocks.pathname,
  useSearchParams: () => {
    if (mocks.suspend) {
      // eslint-disable-next-line @typescript-eslint/only-throw-error
      throw never;
    }
    return new URLSearchParams(mocks.query);
  },
}));
vi.mock("@/lib/auth/actions", () => ({ signOutAction: mocks.signOutAction }));

import { AccountMenu } from "./account-menu";

function stubSession(response: () => Promise<Response>) {
  const fetchMock = vi.fn<typeof fetch>(() => response());
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

async function renderMenu() {
  const view = render(<AccountMenu />);
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  return view;
}

describe("AccountMenu", () => {
  beforeEach(() => {
    mocks.pathname = "/agencies";
    mocks.query = "";
    mocks.suspend = false;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("asks the server for the session without caching", async () => {
    const fetchMock = stubSession(() =>
      Promise.resolve(Response.json({ signedIn: false })),
    );

    await renderMenu();

    expect(fetchMock).toHaveBeenCalledWith("/auth/session", {
      cache: "no-store",
      headers: { accept: "application/json" },
    });
  });

  it("shows nothing until the session is known", () => {
    stubSession(() => new Promise<Response>(() => undefined));

    const { container } = render(<AccountMenu />);

    expect(container).toBeEmptyDOMElement();
  });

  it("links to sign-in, returning to the current page, when signed out", async () => {
    mocks.pathname = "/agencies/la metro";
    stubSession(() => Promise.resolve(Response.json({ signedIn: false })));

    await renderMenu();

    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute(
      "href",
      "/auth/sign-in?returnTo=%2Fagencies%2Fla%20metro",
    );
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("keeps the query string in returnTo", async () => {
    mocks.query = "tab=meetings&page=2";
    stubSession(() => Promise.resolve(Response.json({ signedIn: false })));

    await renderMenu();

    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute(
      "href",
      "/auth/sign-in?returnTo=%2Fagencies%3Ftab%3Dmeetings%26page%3D2",
    );
  });

  it("links with the path alone while the search params are not ready", async () => {
    mocks.suspend = true;
    stubSession(() => Promise.resolve(Response.json({ signedIn: false })));

    await renderMenu();

    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute(
      "href",
      "/auth/sign-in?returnTo=%2Fagencies",
    );
  });

  it("moves focus into the menu when it opens and closes it when focus leaves", async () => {
    stubSession(() =>
      Promise.resolve(Response.json({ signedIn: true, email: null })),
    );
    await renderMenu();
    const button = screen.getByRole("button", { name: "Account" });

    fireEvent.click(button);
    const signOut = screen.getByRole("button", { name: "Sign out" });
    expect(signOut).toHaveFocus();

    fireEvent.blur(signOut, { relatedTarget: button });
    expect(button).toHaveAttribute("aria-expanded", "true");

    fireEvent.blur(signOut, { relatedTarget: document.body });
    expect(button).toHaveAttribute("aria-expanded", "false");
  });

  it("closes when focus leaves the page", async () => {
    stubSession(() =>
      Promise.resolve(Response.json({ signedIn: true, email: null })),
    );
    await renderMenu();
    const button = screen.getByRole("button", { name: "Account" });
    fireEvent.click(button);

    fireEvent.blur(screen.getByRole("button", { name: "Sign out" }));

    expect(button).toHaveAttribute("aria-expanded", "false");
  });

  it("shows the email and a closed menu when signed in", async () => {
    stubSession(() =>
      Promise.resolve(
        Response.json({ signedIn: true, email: "person@example.com" }),
      ),
    );

    await renderMenu();

    const button = screen.getByRole("button", { name: "person@example.com" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "Sign in" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
  });

  it("shows a neutral label without an email", async () => {
    stubSession(() =>
      Promise.resolve(Response.json({ signedIn: true, email: null })),
    );

    await renderMenu();

    expect(screen.getByRole("button", { name: "Account" })).toBeVisible();
  });

  it("opens and closes the menu with the button", async () => {
    stubSession(() =>
      Promise.resolve(Response.json({ signedIn: true, email: null })),
    );
    await renderMenu();
    const button = screen.getByRole("button", { name: "Account" });

    fireEvent.click(button);

    expect(button).toHaveAttribute("aria-expanded", "true");
    const signOut = screen.getByRole("button", { name: "Sign out" });
    expect(signOut).toHaveAttribute("type", "submit");
    expect(signOut.closest("form")).not.toBeNull();
    expect(
      document.getElementById(button.getAttribute("aria-controls") ?? ""),
    ).toContainElement(signOut);

    fireEvent.click(button);

    expect(button).toHaveAttribute("aria-expanded", "false");
  });

  it("closes on Escape and gives focus back to the button", async () => {
    stubSession(() =>
      Promise.resolve(Response.json({ signedIn: true, email: null })),
    );
    await renderMenu();
    const button = screen.getByRole("button", { name: "Account" });
    fireEvent.click(button);
    const signOut = screen.getByRole("button", { name: "Sign out" });
    signOut.focus();

    fireEvent.keyDown(signOut, { key: "Escape" });

    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveFocus();
  });

  it("ignores other keys and Escape while closed", async () => {
    stubSession(() =>
      Promise.resolve(Response.json({ signedIn: true, email: null })),
    );
    await renderMenu();
    const button = screen.getByRole("button", { name: "Account" });

    fireEvent.keyDown(button, { key: "Escape" });
    expect(button).toHaveAttribute("aria-expanded", "false");

    fireEvent.click(button);
    fireEvent.keyDown(button, { key: "Enter" });
    expect(button).toHaveAttribute("aria-expanded", "true");
  });

  it("closes when the pointer goes down outside it", async () => {
    stubSession(() =>
      Promise.resolve(Response.json({ signedIn: true, email: null })),
    );
    await renderMenu();
    const button = screen.getByRole("button", { name: "Account" });
    fireEvent.click(button);

    fireEvent.pointerDown(screen.getByRole("button", { name: "Sign out" }));
    expect(button).toHaveAttribute("aria-expanded", "true");

    fireEvent.pointerDown(document.body);
    expect(button).toHaveAttribute("aria-expanded", "false");
  });

  it.each([
    [
      "an error status",
      () => Promise.resolve(new Response(null, { status: 404 })),
    ],
    ["an unexpected body", () => Promise.resolve(Response.json({ user: "x" }))],
    ["a body that is not JSON", () => Promise.resolve(new Response("<html>"))],
    ["a network error", () => Promise.reject(new TypeError("Failed to fetch"))],
  ])("shows nothing on %s", async (_name, response) => {
    stubSession(response);

    const { container } = await renderMenu();

    expect(container).toBeEmptyDOMElement();
  });

  it("ignores a session that arrives after unmounting", async () => {
    let resolve: (response: Response) => void = () => undefined;
    stubSession(
      () =>
        new Promise<Response>((done) => {
          resolve = done;
        }),
    );
    const { unmount } = render(<AccountMenu />);
    unmount();

    await act(async () => {
      resolve(Response.json({ signedIn: false }));
      await new Promise((done) => setTimeout(done, 0));
    });

    expect(screen.queryByRole("link")).toBeNull();
  });
});
