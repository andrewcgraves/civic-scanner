import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import ErrorPage from "./error";

describe("ErrorPage", () => {
  it("explains the failure and retries on request", () => {
    const retry = vi.fn();
    render(<ErrorPage retry={retry} />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Something went wrong" }),
    ).toBeVisible();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "This page could not be shown.",
    );

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(retry).toHaveBeenCalledOnce();
  });
});
