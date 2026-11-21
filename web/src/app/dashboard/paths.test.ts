import { describe, expect, it } from "vitest";

import {
  DASHBOARD_PATH,
  DASHBOARD_SIGN_IN_PATH,
  SUBSCRIPTIONS_PATH,
} from "./paths";

describe("dashboard paths", () => {
  it("signs in and returns to the dashboard", () => {
    expect(DASHBOARD_PATH).toBe("/dashboard");
    expect(DASHBOARD_SIGN_IN_PATH).toBe("/auth/sign-in?returnTo=%2Fdashboard");
  });

  it("puts subscriptions under the dashboard", () => {
    expect(SUBSCRIPTIONS_PATH).toBe("/dashboard/subscriptions");
  });
});
