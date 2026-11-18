export const FLAG_KEYS = ["test-only-flag"] as const;

export type FlagKey = (typeof FLAG_KEYS)[number];
