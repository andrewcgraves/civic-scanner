import { handleAuth } from "@workos-inc/authkit-nextjs";
import type { NextRequest } from "next/server";

import { getAuthConfig } from "@/lib/auth/config";
import { isSignInEnabled, notFound } from "@/lib/auth/server";

export const dynamic = "force-dynamic";

function signInFailed(): Response {
  return new Response("Sign-in failed. Go back and try again.\n", {
    status: 400,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}

export async function GET(request: NextRequest): Promise<Response> {
  const config = getAuthConfig();
  if (config.kind !== "on" || !(await isSignInEnabled())) {
    return notFound();
  }
  const handler = handleAuth({
    baseURL: config.origin,
    returnPathname: "/",
    onError: () => signInFailed(),
  });
  return handler(request);
}
