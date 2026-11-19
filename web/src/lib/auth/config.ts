export const CALLBACK_PATH = "/auth/callback";
export const MIN_COOKIE_PASSWORD_LENGTH = 32;

export type AuthConfig =
  | { kind: "off" }
  | { kind: "invalid"; problems: readonly string[] }
  | { kind: "on"; origin: string; redirectUri: string };

const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]"]);

function trimmed(value: string | undefined): string {
  return value?.trim() ?? "";
}

function redirectUriProblem(value: string): string | undefined {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return "NEXT_PUBLIC_WORKOS_REDIRECT_URI is not an absolute URL";
  }
  const secure =
    url.protocol === "https:" ||
    (url.protocol === "http:" && LOCAL_HOSTS.has(url.hostname));
  if (!secure) {
    return "NEXT_PUBLIC_WORKOS_REDIRECT_URI must use https (http only for localhost)";
  }
  if (url.username !== "" || url.password !== "") {
    return "NEXT_PUBLIC_WORKOS_REDIRECT_URI must not contain user info";
  }
  if (url.search !== "" || url.hash !== "") {
    return "NEXT_PUBLIC_WORKOS_REDIRECT_URI must not have a query or fragment";
  }
  if (url.pathname !== CALLBACK_PATH) {
    return `NEXT_PUBLIC_WORKOS_REDIRECT_URI must end in ${CALLBACK_PATH}`;
  }
  return undefined;
}

export function readAuthConfig(): AuthConfig {
  const clientId = trimmed(process.env.WORKOS_CLIENT_ID);
  const cookiePassword = process.env.WORKOS_COOKIE_PASSWORD ?? "";
  const redirectUri = trimmed(process.env.NEXT_PUBLIC_WORKOS_REDIRECT_URI);

  if (clientId === "" && cookiePassword === "" && redirectUri === "") {
    return { kind: "off" };
  }

  const problems: string[] = [];
  if (clientId === "") {
    problems.push("WORKOS_CLIENT_ID is not set");
  } else if (!clientId.startsWith("client_")) {
    problems.push("WORKOS_CLIENT_ID does not start with client_");
  }
  if (cookiePassword === "") {
    problems.push("WORKOS_COOKIE_PASSWORD is not set");
  } else if (cookiePassword.length < MIN_COOKIE_PASSWORD_LENGTH) {
    problems.push(
      `WORKOS_COOKIE_PASSWORD is shorter than ${MIN_COOKIE_PASSWORD_LENGTH} characters`,
    );
  }
  if (redirectUri === "") {
    problems.push("NEXT_PUBLIC_WORKOS_REDIRECT_URI is not set");
  } else {
    const problem = redirectUriProblem(redirectUri);
    if (problem !== undefined) {
      problems.push(problem);
    }
  }

  if (problems.length > 0) {
    return { kind: "invalid", problems };
  }
  return { kind: "on", origin: new URL(redirectUri).origin, redirectUri };
}

const LOGGED = Symbol.for("civic-scanner.auth-config-logged");

let cached: AuthConfig | undefined;

export function getAuthConfig(): AuthConfig {
  if (cached === undefined) {
    cached = readAuthConfig();
    const shared = globalThis as { [LOGGED]?: true };
    if (cached.kind === "invalid" && shared[LOGGED] === undefined) {
      shared[LOGGED] = true;
      console.error(
        `Sign-in is disabled because WorkOS is misconfigured: ${cached.problems.join("; ")}`,
      );
    }
  }
  return cached;
}
