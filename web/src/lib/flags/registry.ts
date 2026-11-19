export const FLAG_KEYS = ["accounts", "test-only-flag"] as const;

export type FlagKey = (typeof FLAG_KEYS)[number];
