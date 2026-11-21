import { Notice } from "@/components/ui/notice";

import type { ActionState } from "../state";

export function ActionProblem({ state }: { state: ActionState }) {
  switch (state.kind) {
    case "gone":
      return (
        <Notice tone="error">
          This subscription no longer exists. Reload the page to see your
          current subscriptions.
        </Notice>
      );
    case "upgrade":
      return (
        <Notice tone="error">
          Subscriptions need the paid plan. Start it again to change this one.
        </Notice>
      );
    case "unavailable":
      return (
        <Notice tone="error">
          That could not be done right now. Please try again later.
        </Notice>
      );
    default:
      return null;
  }
}
