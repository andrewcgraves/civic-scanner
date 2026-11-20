import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Badge } from "./badge";

describe("Badge", () => {
  it("renders neutral by default", () => {
    render(<Badge>Draft</Badge>);

    expect(screen.getByText("Draft")).toHaveClass("bg-surface-muted");
  });

  it("uses the tone's colors", () => {
    render(<Badge tone="ai">AI</Badge>);

    expect(screen.getByText("AI")).toHaveClass("bg-ai-surface", "text-ai-ink");
  });
});
