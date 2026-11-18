export type FlagsOn = "all" | readonly string[];

export type FlagSource =
  | { kind: "posthog"; key: string; host: string }
  | { kind: "memory"; on: FlagsOn };

const ALL_FLAGS = "*";
const DEFAULT_POSTHOG_HOST = "https://us.i.posthog.com";

export function readFlagSource(): FlagSource {
  const key = process.env.NEXT_PUBLIC_POSTHOG_KEY?.trim() ?? "";
  if (key !== "") {
    const host = process.env.NEXT_PUBLIC_POSTHOG_HOST?.trim() ?? "";
    return {
      kind: "posthog",
      key,
      host: host === "" ? DEFAULT_POSTHOG_HOST : host,
    };
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
