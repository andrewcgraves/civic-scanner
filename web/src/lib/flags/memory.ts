import type { Provider as ServerProvider } from "@openfeature/server-sdk";
import type { Provider as WebProvider } from "@openfeature/web-sdk";

import { isOn, type FlagsOn } from "./config";

function resolveToDefault<T>(_flagKey: string, defaultValue: T) {
  return { value: defaultValue };
}

function resolveToDefaultAsync<T>(flagKey: string, defaultValue: T) {
  return Promise.resolve(resolveToDefault(flagKey, defaultValue));
}

export function memoryWebProvider(on: FlagsOn): WebProvider {
  return {
    runsOn: "client",
    metadata: { name: "memory" },
    resolveBooleanEvaluation: (flagKey) => ({ value: isOn(on, flagKey) }),
    resolveStringEvaluation: resolveToDefault,
    resolveNumberEvaluation: resolveToDefault,
    resolveObjectEvaluation: resolveToDefault,
  };
}

export function memoryServerProvider(on: FlagsOn): ServerProvider {
  return {
    runsOn: "server",
    metadata: { name: "memory" },
    resolveBooleanEvaluation: (flagKey) =>
      Promise.resolve({ value: isOn(on, flagKey) }),
    resolveStringEvaluation: resolveToDefaultAsync,
    resolveNumberEvaluation: resolveToDefaultAsync,
    resolveObjectEvaluation: resolveToDefaultAsync,
  };
}
