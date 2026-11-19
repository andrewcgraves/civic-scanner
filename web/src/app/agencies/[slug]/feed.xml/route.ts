import { apiBaseUrl } from "@/lib/api/client";

export const dynamic = "force-dynamic";

const SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const MAX_SLUG_LENGTH = 100;
const UPSTREAM_TIMEOUT_MS = 10_000;
const PASSED_RESPONSE_HEADERS = ["content-type", "etag", "cache-control"];

type Method = "GET" | "HEAD";

interface FeedRouteContext {
  params: Promise<{ slug: string }>;
}

export function GET(request: Request, context: FeedRouteContext) {
  return proxyFeed("GET", request, context);
}

export function HEAD(request: Request, context: FeedRouteContext) {
  return proxyFeed("HEAD", request, context);
}

function plainResponse(method: Method, status: number, text: string) {
  return new Response(method === "HEAD" ? null : `${text}\n`, {
    status,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}

async function proxyFeed(
  method: Method,
  request: Request,
  { params }: FeedRouteContext,
): Promise<Response> {
  const { slug } = await params;
  if (slug.length > MAX_SLUG_LENGTH || !SLUG_PATTERN.test(slug)) {
    return plainResponse(method, 404, "Not found");
  }

  const upstreamHeaders = new Headers();
  const ifNoneMatch = request.headers.get("if-none-match");
  if (ifNoneMatch !== null) {
    upstreamHeaders.set("if-none-match", ifNoneMatch);
  }

  let upstream: Response;
  try {
    upstream = await fetch(`${apiBaseUrl}/agencies/${slug}/feed.xml`, {
      method,
      headers: upstreamHeaders,
      redirect: "manual",
      cache: "no-store",
      signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS),
    });
  } catch {
    return plainResponse(method, 502, "Feed unavailable");
  }

  const redirected = upstream.status >= 300 && upstream.status < 400;
  if (
    upstream.type === "opaqueredirect" ||
    (redirected && upstream.status !== 304)
  ) {
    return plainResponse(method, 502, "Feed unavailable");
  }

  const headers = new Headers();
  for (const name of PASSED_RESPONSE_HEADERS) {
    const value = upstream.headers.get(name);
    if (value !== null) {
      headers.set(name, value);
    }
  }
  const body =
    method === "HEAD" || upstream.status === 304 ? null : upstream.body;
  return new Response(body, { status: upstream.status, headers });
}
