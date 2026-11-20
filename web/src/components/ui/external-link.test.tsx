import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ExternalLink, isWebUrl } from "./external-link";

describe("ExternalLink", () => {
  it("links out with safe rel attributes and says it leaves the site", () => {
    render(
      <ExternalLink href="https://www.metro.net/agenda.pdf">
        Agenda
      </ExternalLink>,
    );

    const link = screen.getByRole("link", {
      name: /^Agenda\s*\(leaves Civic Scanner\)$/,
    });
    expect(link).toHaveAttribute("href", "https://www.metro.net/agenda.pdf");
    expect(link).toHaveAttribute("rel", "external noopener noreferrer");
    expect(link).not.toHaveAttribute("target");
  });

  it.each([
    "javascript:alert(1)",
    "data:text/html,x",
    "/relative",
    "not a url",
  ])("renders %j as plain text, not a link", (href) => {
    render(<ExternalLink href={href}>Agenda</ExternalLink>);

    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("Agenda")).toBeVisible();
  });
});

describe("isWebUrl", () => {
  it("accepts only http and https URLs", () => {
    expect(isWebUrl("http://example.com")).toBe(true);
    expect(isWebUrl("https://example.com")).toBe(true);
    expect(isWebUrl("ftp://example.com")).toBe(false);
  });
});
