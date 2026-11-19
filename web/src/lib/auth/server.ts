import "server-only";

import { isFlagEnabled } from "@/lib/flags/server";

import { getAuthConfig } from "./config";

export async function isSignInEnabled(): Promise<boolean> {
  return getAuthConfig().kind === "on" && (await isFlagEnabled("accounts"));
}

export function notFound(): Response {
  return new Response("Not found\n", {
    status: 404,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}
