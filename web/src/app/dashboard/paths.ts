export const DASHBOARD_PATH = "/dashboard";

export const DASHBOARD_SIGN_IN_PATH = `/auth/sign-in?returnTo=${encodeURIComponent(DASHBOARD_PATH)}`;

export const SUBSCRIPTIONS_PATH = `${DASHBOARD_PATH}/subscriptions`;
