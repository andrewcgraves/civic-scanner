import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import Loading from "./loading";

describe("Loading", () => {
  it("announces that the page is loading", () => {
    render(<Loading />);

    expect(screen.getByRole("status")).toHaveTextContent("Loading…");
  });
});
