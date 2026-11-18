"use client";

import { createContext, useContext, type ReactNode } from "react";

import { isOn, type FlagsOn } from "./config";

const FlagsContext = createContext<FlagsOn>([]);

export function FlagsProvider({
  on,
  children,
}: {
  on: FlagsOn;
  children: ReactNode;
}) {
  return <FlagsContext value={on}>{children}</FlagsContext>;
}

export function useFlag(flag: string): boolean {
  return isOn(useContext(FlagsContext), flag);
}
