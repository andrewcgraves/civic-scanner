const FALLBACK = "/";
const MAX_LENGTH = 2048;
const PROBE_ORIGIN = "https://return-path.invalid";

function hasUnsafeCharacter(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code < 0x21 || code === 0x7f || code === 0x5c) {
      return true;
    }
  }
  return false;
}

function isSafePath(value: string): boolean {
  return (
    value.length <= MAX_LENGTH &&
    value.startsWith("/") &&
    !value.startsWith("//") &&
    !hasUnsafeCharacter(value)
  );
}

function isAuthPath(pathname: string): boolean {
  const lower = pathname.toLowerCase();
  return lower === "/auth" || lower.startsWith("/auth/");
}

const DOT_SEGMENT = /(^|\/)\.\.?(\/|$)/;

function decoded(pathname: string): string | null {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return null;
  }
}

export function safeReturnPath(value: string | null | undefined): string {
  if (typeof value !== "string" || !isSafePath(value)) {
    return FALLBACK;
  }

  const url = new URL(value, PROBE_ORIGIN);
  const normalized = `${url.pathname}${url.search}`;
  const decodedPath = decoded(url.pathname);
  if (
    url.origin !== PROBE_ORIGIN ||
    !isSafePath(normalized) ||
    decodedPath === null ||
    decodedPath.startsWith("//") ||
    decodedPath.includes("\\") ||
    DOT_SEGMENT.test(decodedPath) ||
    isAuthPath(url.pathname) ||
    isAuthPath(decodedPath)
  ) {
    return FALLBACK;
  }
  return normalized;
}
