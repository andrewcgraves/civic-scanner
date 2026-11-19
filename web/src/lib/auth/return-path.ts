const FALLBACK = "/";
const MAX_LENGTH = 2048;
const PROBE_ORIGIN = "https://return-path.invalid";
function hasUnsafeCharacter(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code < 0x20 || code === 0x7f || code === 0x5c) {
      return true;
    }
  }
  return false;
}

export function safeReturnPath(value: string | null | undefined): string {
  if (
    typeof value !== "string" ||
    value.length > MAX_LENGTH ||
    !value.startsWith("/") ||
    value.startsWith("//") ||
    hasUnsafeCharacter(value)
  ) {
    return FALLBACK;
  }

  const url = new URL(value, PROBE_ORIGIN);
  if (
    url.origin !== PROBE_ORIGIN ||
    url.pathname === "/auth" ||
    url.pathname.startsWith("/auth/")
  ) {
    return FALLBACK;
  }
  return `${url.pathname}${url.search}`;
}
