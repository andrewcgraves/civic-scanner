import { withAuth } from "@workos-inc/authkit-nextjs";
import { cookies, headers } from "next/headers";

import { callApiAsUser } from "@/lib/auth/api";
import { SESSION_STATE_HEADER, sessionCookieName } from "@/lib/auth/cookie";
import { isSignInEnabled, notFound } from "@/lib/auth/server";
import type { SessionState } from "@/lib/auth/session-state";

export const dynamic = "force-dynamic";

function sessionResponse(state: SessionState): Response {
  return Response.json(state, {
    headers: { "cache-control": "private, no-store", vary: "Cookie" },
  });
}

export async function GET(): Promise<Response> {
  if (!(await isSignInEnabled())) {
    return notFound();
  }
  if (!(await cookies()).has(sessionCookieName())) {
    return sessionResponse({ signedIn: false });
  }
  const { user } = await withAuth();
  if (user === null) {
    const unverified =
      (await headers()).get(SESSION_STATE_HEADER) === "unverified";
    return sessionResponse(
      unverified
        ? { signedIn: true, email: null, role: null }
        : { signedIn: false },
    );
  }

  const me = await callApiAsUser((api) => api.GET("/me"));
  if (me.status === "signed-out") {
    return sessionResponse({ signedIn: false });
  }
  return sessionResponse({
    signedIn: true,
    email: me.status === "ok" ? me.data.email : null,
    role: me.status === "ok" ? me.data.role : null,
  });
}
