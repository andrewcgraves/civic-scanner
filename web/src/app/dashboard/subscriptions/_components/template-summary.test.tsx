import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { TemplateSummary } from "./template-summary";

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";
const TEMPLATE = '{\n  "title": "{{meeting.title}}"\n}';

describe("TemplateSummary", () => {
  it("shows the saved template with a link to edit it", () => {
    render(
      <TemplateSummary id={ID} agencyName="LA Metro" template={TEMPLATE} />,
    );

    expect(screen.getByRole("heading", { name: "Template" })).toBeVisible();
    expect(screen.getByText(/placeholders filled in/)).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Edit the template for LA Metro" }),
    ).toHaveAttribute("href", `/dashboard/subscriptions/${ID}/edit#template`);
    expect(screen.getByText(/"title"/).textContent).toBe(TEMPLATE);
    expect(screen.getByText(/"title"/).closest("details")).toHaveAttribute(
      "open",
    );
  });
});
