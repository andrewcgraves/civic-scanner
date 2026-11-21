import {
  applyResponseHeaders,
  authkit,
  partitionAuthkitHeaders,
} from "@workos-inc/authkit-nextjs";
import { NextResponse, type NextRequest } from "next/server";

import { getAuthConfig } from "@/lib/auth/config";
import {
  SESSION_STATE_HEADER,
  sessionCookieName,
  type ProxySessionState,
} from "@/lib/auth/cookie";

const VERIFIER_COOKIE_PREFIX = "wos-auth-verifier";

function passThrough(request: NextRequest): NextResponse {
  const headers = new Headers(request.headers);
  headers.delete(SESSION_STATE_HEADER);
  return NextResponse.next({ request: { headers } });
}

function withoutVerifierCookies(headers: Headers): Headers {
  const kept = new Headers();
  for (const [name, value] of headers) {
    if (name !== "set-cookie") {
      kept.append(name, value);
    }
  }
  for (const cookie of headers.getSetCookie()) {
    if (!cookie.startsWith(VERIFIER_COOKIE_PREFIX)) {
      kept.append("set-cookie", cookie);
    }
  }
  return kept;
}

export async function proxy(request: NextRequest): Promise<NextResponse> {
  const config = getAuthConfig();
  const cookieName = sessionCookieName();
  if (config.kind !== "on" || !request.cookies.has(cookieName)) {
    return passThrough(request);
  }

  let refreshed = false;
  const { session, headers } = await authkit(request, {
    redirectUri: config.redirectUri,
    onSessionRefreshSuccess: () => {
      refreshed = true;
    },
  });
  const { requestHeaders, responseHeaders } = partitionAuthkitHeaders(
    request,
    headers,
  );
  const sent = withoutVerifierCookies(responseHeaders);
  const cleared = sent
    .getSetCookie()
    .some((cookie) => cookie.startsWith(`${cookieName}=;`));

  let state: ProxySessionState | undefined;
  if (refreshed) {
    state = "refreshed";
  } else if (session.user === null && !cleared) {
    state = "unverified";
  }
  requestHeaders.delete(SESSION_STATE_HEADER);
  if (state !== undefined) {
    requestHeaders.set(SESSION_STATE_HEADER, state);
  }

  return applyResponseHeaders(
    NextResponse.next({ request: { headers: requestHeaders } }),
    sent,
  );
}

export const config = {
  matcher: [
    "/auth/session",
    "/billing",
    "/dashboard",
    "/submit-agency",
    "/admin/agency-submissions",
  ],
};
