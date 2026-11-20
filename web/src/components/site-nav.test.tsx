import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const navigation = vi.hoisted(() => ({ pathname: "/" }));

vi.mock("next/navigation", () => ({ usePathname: () => navigation.pathname }));

import { SiteNav } from "./site-nav";

describe("SiteNav", () => {
  afterEach(() => {
    navigation.pathname = "/";
  });

  it("links to the agencies page", () => {
    render(<SiteNav />);

    const nav = screen.getByRole("navigation", { name: "Primary" });
    const link = screen.getByRole("link", { name: "Agencies" });
    expect(nav).toContainElement(link);
    expect(link).toHaveAttribute("href", "/agencies");
    expect(link).not.toHaveAttribute("aria-current");
  });

  it.each(["/agencies", "/agencies/la-metro"])(
    "marks Agencies current on %s",
    (pathname) => {
      navigation.pathname = pathname;
      render(<SiteNav />);

      expect(screen.getByRole("link", { name: "Agencies" })).toHaveAttribute(
        "aria-current",
        "page",
      );
    },
  );

  it("does not mark Agencies current on a path that only starts the same", () => {
    navigation.pathname = "/agencies-old";
    render(<SiteNav />);

    expect(screen.getByRole("link", { name: "Agencies" })).not.toHaveAttribute(
      "aria-current",
    );
  });
});
