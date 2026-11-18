export interface PostHogProject {
  key: string;
  host: string;
}

const DEFAULT_POSTHOG_HOST = "https://us.i.posthog.com";

export function readPostHogProject(): PostHogProject | undefined {
  const key = process.env.NEXT_PUBLIC_POSTHOG_KEY?.trim() ?? "";
  if (key === "") {
    return undefined;
  }
  const host = process.env.NEXT_PUBLIC_POSTHOG_HOST?.trim() ?? "";
  return { key, host: host === "" ? DEFAULT_POSTHOG_HOST : host };
}
