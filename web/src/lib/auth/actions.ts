"use server";

import { signOut } from "@workos-inc/authkit-nextjs";
import { redirect } from "next/navigation";

import { getAuthConfig } from "./config";
import { isSignInEnabled } from "./server";

export async function signOutAction(): Promise<void> {
  const config = getAuthConfig();
  if (config.kind !== "on" || !(await isSignInEnabled())) {
    redirect("/");
  }
  await signOut({ returnTo: `${config.origin}/` });
}
