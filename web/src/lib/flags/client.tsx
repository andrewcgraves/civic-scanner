"use client";

import { createContext, useContext, type ReactNode } from "react";

import type { FlagKey } from "./registry";

const FlagsContext = createContext<readonly FlagKey[]>([]);

export function FlagsProvider({
  on,
  children,
}: {
  on: readonly FlagKey[];
  children: ReactNode;
}) {
  return <FlagsContext value={on}>{children}</FlagsContext>;
}

export function useFlag(flag: FlagKey): boolean {
  return useContext(FlagsContext).includes(flag);
}
