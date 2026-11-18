import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AgencyList } from "./agency-list";

describe("AgencyList", () => {
  it("renders one item per agency", () => {
    render(
      <AgencyList
        agencies={[
          {
            id: "7f1c1f0e-5d1b-4c62-9d55-0d6b6c2d9a01",
            slug: "la-metro",
            name: "LA Metro",
            website: "https://www.metro.net",
          },
        ]}
      />,
    );

    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText("LA Metro")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "metro.net" })).toHaveAttribute(
      "href",
      "https://www.metro.net",
    );
  });

  it("renders an agency without a link when its website is not a valid URL", () => {
    render(
      <AgencyList
        agencies={[
          {
            id: "7f1c1f0e-5d1b-4c62-9d55-0d6b6c2d9a01",
            slug: "la-metro",
            name: "LA Metro",
            website: "",
          },
        ]}
      />,
    );

    expect(screen.getByText("LA Metro")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("renders an empty state", () => {
    render(<AgencyList agencies={[]} />);

    expect(
      screen.getByText("No agencies are tracked yet."),
    ).toBeInTheDocument();
  });
});
