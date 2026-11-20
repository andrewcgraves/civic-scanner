import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PageHeader } from "./page-header";

describe("PageHeader", () => {
  it("renders only the title when nothing else is given", () => {
    const { container } = render(<PageHeader title="Agencies" />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Agencies" }),
    ).toBeVisible();
    expect(container.querySelector("header")?.children).toHaveLength(1);
  });

  it("renders breadcrumb, eyebrow, description and extra content", () => {
    render(
      <PageHeader
        title="Board meeting"
        eyebrow="LA Metro"
        description="The regular monthly meeting."
        breadcrumb={[
          { label: "Agencies", href: "/agencies" },
          { label: "Board meeting" },
        ]}
      >
        <span>Held</span>
      </PageHeader>,
    );

    expect(
      screen.getByRole("navigation", { name: "Breadcrumb" }),
    ).toBeVisible();
    expect(screen.getByText("LA Metro")).toBeVisible();
    expect(screen.getByText("The regular monthly meeting.")).toBeVisible();
    expect(screen.getByText("Held")).toBeVisible();
  });
});
