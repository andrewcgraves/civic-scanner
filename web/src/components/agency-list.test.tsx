import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Agency } from "@/lib/api/client";

import { AgencyList } from "./agency-list";

const laMetro: Agency = {
  id: "7f1c1f0e-5d1b-4c62-9d55-0d6b6c2d9a01",
  slug: "la-metro",
  name: "LA Metro",
  website: "https://www.metro.net",
};

const metrolink: Agency = {
  id: "0b6f3c55-8f8e-4a0e-9a55-2f4c1f9d7b02",
  slug: "metrolink",
  name: "Metrolink",
  website: "https://metrolinktrains.com",
};

describe("AgencyList", () => {
  describe("when agency pages are on", () => {
    it("links each agency to its page and shows its website", () => {
      render(<AgencyList agencies={[laMetro, metrolink]} linkToAgencyPages />);

      const list = screen.getByRole("list", { name: "Agencies" });
      expect(within(list).getAllByRole("listitem")).toHaveLength(2);
      expect(
        within(list).getByRole("link", { name: /LA Metro/ }),
      ).toHaveAttribute("href", "/agencies/la-metro");
      expect(
        within(list).getByRole("link", { name: /Metrolink/ }),
      ).toHaveAttribute("href", "/agencies/metrolink");
      expect(within(list).getByText("metro.net")).toBeInTheDocument();
      expect(within(list).getAllByRole("link")).toHaveLength(2);
    });

    it("leaves out a website that is not a web URL", () => {
      render(
        <AgencyList
          agencies={[{ ...laMetro, website: "javascript:alert(1)" }]}
          linkToAgencyPages
        />,
      );

      const link = screen.getByRole("link", { name: "LA Metro" });
      expect(link).toHaveAttribute("href", "/agencies/la-metro");
      expect(link).toHaveTextContent(/^LA Metro$/);
    });
  });

  describe("when agency pages are off", () => {
    it("shows each agency with only its website as a link", () => {
      render(
        <AgencyList
          agencies={[laMetro, metrolink]}
          linkToAgencyPages={false}
        />,
      );

      const list = screen.getByRole("list", { name: "Agencies" });
      expect(within(list).getAllByRole("listitem")).toHaveLength(2);
      expect(within(list).getByText("LA Metro")).toBeInTheDocument();
      const links = within(list).getAllByRole("link");
      expect(links).toHaveLength(2);
      expect(links[0]).toHaveAttribute("href", "https://www.metro.net");
      expect(links[0]).toHaveAccessibleName(/metro\.net/);
      expect(links[1]).toHaveAttribute("href", "https://metrolinktrains.com");
      expect(
        screen.queryByRole("link", { name: /LA Metro/ }),
      ).not.toBeInTheDocument();
    });

    it("shows an agency without a link when its website is not a web URL", () => {
      render(
        <AgencyList
          agencies={[
            { ...laMetro, website: "" },
            { ...metrolink, website: "javascript:alert(1)" },
          ]}
          linkToAgencyPages={false}
        />,
      );

      expect(screen.getByText("LA Metro")).toBeInTheDocument();
      expect(screen.getByText("Metrolink")).toBeInTheDocument();
      expect(screen.queryByRole("link")).not.toBeInTheDocument();
    });
  });

  it.each([true, false])(
    "shows an empty state when there are no agencies (pages on: %s)",
    (linkToAgencyPages) => {
      render(
        <AgencyList agencies={[]} linkToAgencyPages={linkToAgencyPages} />,
      );

      expect(
        screen.getByText("No agencies are covered yet."),
      ).toBeInTheDocument();
      expect(screen.queryByRole("list")).not.toBeInTheDocument();
    },
  );
});
