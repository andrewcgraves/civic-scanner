import { readPostHogProject } from "@/lib/posthog/config";

export type FlagsOn = "all" | readonly string[];

export type FlagSource =
  | { kind: "posthog"; key: string; host: string }
  | { kind: "memory"; on: FlagsOn };

const ALL_FLAGS = "*";

export function readFlagSource(): FlagSource {
  const posthog = readPostHogProject();
  if (posthog !== undefined) {
    return { kind: "posthog", ...posthog };
  }

  const listed = (process.env.NEXT_PUBLIC_FEATURE_FLAGS ?? "")
    .split(",")
    .map((flag) => flag.trim())
    .filter((flag) => flag !== "");
  return {
    kind: "memory",
    on: listed.includes(ALL_FLAGS) ? "all" : listed,
  };
}

export function isOn(on: FlagsOn, flag: string): boolean {
  return on === "all" || on.includes(flag);
}
