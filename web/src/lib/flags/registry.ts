export const FLAG_KEYS = [
  "accounts",
  "agency-pages",
  "meeting-pages",
  "meeting-transcripts",
  "test-only-flag",
] as const;

export type FlagKey = (typeof FLAG_KEYS)[number];
