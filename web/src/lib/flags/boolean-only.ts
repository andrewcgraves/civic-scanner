import {
  StandardResolutionReasons,
  type ResolutionDetails,
} from "@openfeature/server-sdk";

function resolveToDefault<T>(
  _flagKey: string,
  defaultValue: T,
): Promise<ResolutionDetails<T>> {
  return Promise.resolve({
    value: defaultValue,
    reason: StandardResolutionReasons.DEFAULT,
  });
}

export const nonBooleanResolvers = {
  resolveStringEvaluation: resolveToDefault<string>,
  resolveNumberEvaluation: resolveToDefault<number>,
  resolveObjectEvaluation: resolveToDefault,
};
