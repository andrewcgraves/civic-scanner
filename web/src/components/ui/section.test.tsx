import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Section } from "./section";

describe("Section", () => {
  it("is a region named by its level-two heading", () => {
    render(
      <Section title="Agenda">
        <p>body</p>
      </Section>,
    );

    const region = screen.getByRole("region", { name: "Agenda" });
    expect(region).toContainElement(screen.getByText("body"));
    expect(
      screen.getByRole("heading", { level: 2, name: "Agenda" }),
    ).toBeVisible();
  });

  it("renders a level-three heading, an id, a description and actions", () => {
    render(
      <Section
        title="Transcript"
        level={3}
        id="transcript"
        description="What was said."
        actions={<a href="#top">Top</a>}
      >
        <p>body</p>
      </Section>,
    );

    expect(
      screen.getByRole("heading", { level: 3, name: "Transcript" }),
    ).toBeVisible();
    expect(screen.getByRole("region", { name: "Transcript" })).toHaveAttribute(
      "id",
      "transcript",
    );
    expect(screen.getByText("What was said.")).toBeVisible();
    expect(screen.getByRole("link", { name: "Top" })).toBeVisible();
  });

  it("renders a level-four heading", () => {
    render(
      <Section title="Summary" level={4}>
        <p>body</p>
      </Section>,
    );

    expect(
      screen.getByRole("heading", { level: 4, name: "Summary" }),
    ).toBeVisible();
    expect(screen.getByRole("region", { name: "Summary" })).toContainElement(
      screen.getByText("body"),
    );
  });
});
