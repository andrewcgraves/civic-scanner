"use server";

import { signOut } from "@workos-inc/authkit-nextjs";
import { redirect } from "next/navigation";

import { isSignInEnabled } from "./server";

export async function signOutAction(): Promise<void> {
  if (!(await isSignInEnabled())) {
    redirect("/");
  }
  await signOut();
}
