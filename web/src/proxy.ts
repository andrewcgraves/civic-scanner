import { authkitProxy } from "@workos-inc/authkit-nextjs";
import {
  NextResponse,
  type NextFetchEvent,
  type NextRequest,
} from "next/server";

import { getAuthConfig } from "@/lib/auth/config";
import { sessionCookieName } from "@/lib/auth/cookie";

const handleSession = authkitProxy();

export function proxy(request: NextRequest, event: NextFetchEvent) {
  if (
    getAuthConfig().kind !== "on" ||
    !request.cookies.has(sessionCookieName())
  ) {
    return NextResponse.next();
  }
  return handleSession(request, event);
}

export const config = { matcher: ["/auth/session"] };
