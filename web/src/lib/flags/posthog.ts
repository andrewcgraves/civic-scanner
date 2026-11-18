import {
  FlagNotFoundError,
  StandardResolutionReasons,
  TypeMismatchError,
  type Provider,
  type ResolutionDetails,
} from "@openfeature/server-sdk";
import { PostHog } from "posthog-node";

import { nonBooleanResolvers } from "./boolean-only";

const REQUEST_TIMEOUT_MS = 500;
const CACHE_TTL_MS = 30_000;

type Answers = Readonly<Record<string, boolean | string | undefined>>;

function reportFailure(error: unknown) {
  console.error("Feature flags could not be evaluated", error);
}

export class PostHogProvider implements Provider {
  readonly runsOn = "server";
  readonly metadata = { name: "posthog-all-flags" } as const;

  private readonly client: PostHog;
  private readonly distinctId: string;
  private cached: { answers: Promise<Answers>; expiresAt: number } | undefined;

  constructor(options: { key: string; host: string; distinctId: string }) {
    this.client = new PostHog(options.key, {
      host: options.host,
      featureFlagsRequestTimeoutMs: REQUEST_TIMEOUT_MS,
      featureFlagsRequestMaxRetries: 0,
    });
    this.client.on("error", reportFailure);
    this.distinctId = options.distinctId;
  }

  async resolveBooleanEvaluation(
    flagKey: string,
  ): Promise<ResolutionDetails<boolean>> {
    const answer = (await this.answers())[flagKey];
    if (answer === undefined) {
      throw new FlagNotFoundError(`PostHog has no flag '${flagKey}'`);
    }
    if (typeof answer !== "boolean") {
      throw new TypeMismatchError(`PostHog flag '${flagKey}' is multivariate`);
    }
    return { value: answer, reason: StandardResolutionReasons.CACHED };
  }

  resolveStringEvaluation = nonBooleanResolvers.resolveStringEvaluation;
  resolveNumberEvaluation = nonBooleanResolvers.resolveNumberEvaluation;
  resolveObjectEvaluation = nonBooleanResolvers.resolveObjectEvaluation;

  private answers(): Promise<Answers> {
    const now = Date.now();
    if (this.cached === undefined || this.cached.expiresAt <= now) {
      this.cached = { answers: this.ask(), expiresAt: now + CACHE_TTL_MS };
    }
    return this.cached.answers;
  }

  private async ask(): Promise<Answers> {
    try {
      const flags = await this.client.evaluateFlags(this.distinctId);
      return Object.fromEntries(
        flags.keys.map((flag) => [
          flag,
          flags.getFlag(flag, { sendFeatureFlagEvents: false }),
        ]),
      );
    } catch (error) {
      reportFailure(error);
      return {};
    }
  }
}
