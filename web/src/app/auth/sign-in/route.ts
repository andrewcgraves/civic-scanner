import { getSignInUrl } from "@workos-inc/authkit-nextjs";
import type { NextRequest } from "next/server";

import { getAuthConfig } from "@/lib/auth/config";
import { safeReturnPath } from "@/lib/auth/return-path";
import { isSignInEnabled, notFound } from "@/lib/auth/server";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest): Promise<Response> {
  const config = getAuthConfig();
  if (config.kind !== "on" || !(await isSignInEnabled())) {
    return notFound();
  }
  const returnTo = safeReturnPath(request.nextUrl.searchParams.get("returnTo"));
  const location = await getSignInUrl({
    returnTo,
    redirectUri: config.redirectUri,
  });
  return new Response(null, {
    status: 307,
    headers: { location, "cache-control": "no-store" },
  });
}
