export const FLAG_KEYS = [
  "accounts",
  "agency-pages",
  "agency-rss-feeds",
  "agency-submissions",
  "billing",
  "meeting-pages",
  "meeting-transcripts",
  "test-only-flag",
] as const;

export type FlagKey = (typeof FLAG_KEYS)[number];
