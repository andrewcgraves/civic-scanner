import {
  StandardResolutionReasons,
  type Provider,
} from "@openfeature/server-sdk";

import { nonBooleanResolvers } from "./boolean-only";
import { isOn, type FlagsOn } from "./config";

export function memoryProvider(on: FlagsOn): Provider {
  return {
    runsOn: "server",
    metadata: { name: "memory" },
    resolveBooleanEvaluation: (flagKey) =>
      Promise.resolve({
        value: isOn(on, flagKey),
        reason: StandardResolutionReasons.STATIC,
      }),
    ...nonBooleanResolvers,
  };
}
