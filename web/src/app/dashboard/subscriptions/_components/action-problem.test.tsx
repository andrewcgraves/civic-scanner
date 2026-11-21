import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { ActionState } from "../state";

import { ActionProblem } from "./action-problem";

describe("ActionProblem", () => {
  it.each<[ActionState, RegExp]>([
    [{ kind: "gone" }, /no longer exists/],
    [{ kind: "upgrade" }, /need the paid plan/],
    [{ kind: "unavailable" }, /could not be done right now/],
  ])("explains %j", (state, text) => {
    render(<ActionProblem state={state} />);

    expect(screen.getByRole("alert")).toHaveTextContent(text);
  });

  it.each<ActionState>([{ kind: "idle" }, { kind: "rotated", secret: "s" }])(
    "says nothing for %j",
    (state) => {
      const { container } = render(<ActionProblem state={state} />);

      expect(container).toBeEmptyDOMElement();
    },
  );
});
