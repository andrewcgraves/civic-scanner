import { createSign, generateKeyPairSync } from "node:crypto";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

import { NextRequest } from "next/server";
import {
  afterAll,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";

import type * as CallbackRoute from "@/app/auth/callback/route";
import type * as SignInRoute from "@/app/auth/sign-in/route";

const CLIENT_ID = "client_01SDKTEST";
const COOKIE_PASSWORD = "sdk-test-cookie-password-0123456789abcdef";
const REDIRECT_URI = "https://civic.example/auth/callback";
const EMAIL = "person@example.com";

interface StoredCookie {
  value: string;
  options?: Record<string, unknown>;
}

const store = vi.hoisted(() => ({
  cookies: new Map<string, StoredCookie>(),
  headers: new Headers(),
}));

vi.mock("next/headers", () => ({
  cookies: () =>
    Promise.resolve({
      get: (name: string) => {
        const cookie = store.cookies.get(name);
        return cookie === undefined ? undefined : { name, value: cookie.value };
      },
      getAll: () =>
        [...store.cookies].map(([name, cookie]) => ({
          name,
          value: cookie.value,
        })),
      has: (name: string) => store.cookies.has(name),
      set: (name: string, value: string, options?: Record<string, unknown>) => {
        store.cookies.set(name, { value, options });
      },
      delete: (cookie: string | { name: string }) => {
        store.cookies.delete(typeof cookie === "string" ? cookie : cookie.name);
      },
    }),
  headers: () => Promise.resolve(store.headers),
}));

const keys = generateKeyPairSync("rsa", { modulusLength: 2048 });
const jwk = {
  ...keys.publicKey.export({ format: "jwk" }),
  kid: "k1",
  alg: "RS256",
  use: "sig",
};

function base64url(value: string | Buffer): string {
  return Buffer.from(value).toString("base64url");
}

function accessToken(): string {
  const now = Math.floor(Date.now() / 1000);
  const header = base64url(
    JSON.stringify({ alg: "RS256", kid: "k1", typ: "JWT" }),
  );
  const payload = base64url(
    JSON.stringify({
      sub: "user_01",
      sid: "session_01",
      client_id: CLIENT_ID,
      iat: now,
      exp: now + 3600,
      auth_time: now,
    }),
  );
  const signer = createSign("RSA-SHA256");
  signer.update(`${header}.${payload}`);
  return `${header}.${payload}.${base64url(signer.sign(keys.privateKey))}`;
}

const workosUser = {
  object: "user",
  id: "user_01",
  email: EMAIL,
  email_verified: true,
  first_name: "Person",
  last_name: "Example",
  profile_picture_url: null,
  last_sign_in_at: "2026-11-18T00:00:00.000Z",
  created_at: "2026-11-01T00:00:00.000Z",
  updated_at: "2026-11-18T00:00:00.000Z",
  external_id: null,
  metadata: {},
  locale: null,
};

interface StubRequest {
  method: string;
  url: string;
  body: string;
}

const requests: StubRequest[] = [];
let issuedToken = "";
let server: Server;

let signIn: typeof SignInRoute.GET;
let callback: typeof CallbackRoute.GET;

beforeAll(async () => {
  server = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk: Buffer) => {
      body += chunk.toString();
    });
    request.on("end", () => {
      requests.push({
        method: request.method ?? "",
        url: request.url ?? "",
        body,
      });
      if (request.url === `/sso/jwks/${CLIENT_ID}`) {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(JSON.stringify({ keys: [jwk] }));
        return;
      }
      if (request.url === "/user_management/authenticate") {
        issuedToken = accessToken();
        response.writeHead(200, { "content-type": "application/json" });
        response.end(
          JSON.stringify({
            user: workosUser,
            access_token: issuedToken,
            refresh_token: "refresh-token-1",
          }),
        );
        return;
      }
      response.writeHead(404);
      response.end();
    });
  });
  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve);
  });
  const { port } = server.address() as AddressInfo;

  vi.stubEnv("WORKOS_CLIENT_ID", CLIENT_ID);
  vi.stubEnv("WORKOS_COOKIE_PASSWORD", COOKIE_PASSWORD);
  vi.stubEnv("NEXT_PUBLIC_WORKOS_REDIRECT_URI", REDIRECT_URI);
  vi.stubEnv("WORKOS_API_KEY", undefined);
  vi.stubEnv("WORKOS_API_HOSTNAME", "127.0.0.1");
  vi.stubEnv("WORKOS_API_HTTPS", "false");
  vi.stubEnv("WORKOS_API_PORT", String(port));
  vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", undefined);
  vi.stubEnv("NEXT_PUBLIC_FEATURE_FLAGS", "accounts");
  vi.resetModules();
  signIn = (await import("@/app/auth/sign-in/route")).GET;
  callback = (await import("@/app/auth/callback/route")).GET;
});

afterAll(async () => {
  vi.unstubAllEnvs();
  const { OpenFeature } = await import("@openfeature/server-sdk");
  await OpenFeature.clearProviders();
  await new Promise<void>((resolve) => {
    server.close(() => {
      resolve();
    });
  });
});

beforeEach(() => {
  store.cookies.clear();
  store.headers = new Headers();
  requests.length = 0;
});

async function startSignIn(returnTo: string) {
  const response = await signIn(
    new NextRequest(
      `https://civic.example/auth/sign-in?returnTo=${encodeURIComponent(returnTo)}`,
    ),
  );
  const location = new URL(response.headers.get("location") ?? "");
  const verifiers = [...store.cookies].filter(([name]) =>
    name.startsWith("wos-auth-verifier-"),
  );
  return { response, location, verifiers };
}

async function finishSignIn(state: string, cookieHeader: string) {
  return callback(
    new NextRequest(
      `https://civic.example/auth/callback?code=code-1&state=${encodeURIComponent(state)}`,
      { headers: { cookie: cookieHeader } },
    ),
  );
}

describe("AuthKit, unmocked", () => {
  it("builds the authorization URL locally, on the configured redirect URI, ignoring x-redirect-uri", async () => {
    store.headers = new Headers({
      "x-redirect-uri": "https://evil.example/cb",
    });

    const { response, location, verifiers } = await startSignIn("/agencies");

    expect(response.status).toBe(307);
    expect(location.pathname).toBe("/user_management/authorize");
    expect(location.searchParams.get("client_id")).toBe(CLIENT_ID);
    expect(location.searchParams.get("redirect_uri")).toBe(REDIRECT_URI);
    expect(location.searchParams.get("code_challenge_method")).toBe("S256");
    expect(location.searchParams.get("provider")).toBe("authkit");
    expect(response.headers.get("location")).not.toContain("evil.example");
    expect(requests).toEqual([]);

    expect(verifiers).toHaveLength(1);
    const [, verifier] = verifiers[0] ?? [];
    expect(verifier?.value).toBe(location.searchParams.get("state"));
    expect(verifier?.value).not.toContain("agencies");
    expect(verifier?.options).toMatchObject({
      httpOnly: true,
      secure: true,
      sameSite: "lax",
      path: "/",
      maxAge: 600,
    });
  });

  it.each([
    ["/agencies?tab=meetings", "https://civic.example/agencies?tab=meetings"],
    ["/.//evil.example", "https://civic.example/"],
    ["/x/..//evil.example", "https://civic.example/"],
    ["https://evil.example/", "https://civic.example/"],
  ])(
    "round-trips returnTo %j through the sealed state to %s",
    async (returnTo, expected) => {
      const { location, verifiers } = await startSignIn(returnTo);
      const [name, verifier] = verifiers[0] ?? [];
      const state = location.searchParams.get("state") ?? "";

      const response = await finishSignIn(state, `${name}=${verifier?.value}`);

      expect(response.status).toBe(307);
      expect(response.headers.get("location")).toBe(expected);
    },
  );

  it("exchanges the code with the PKCE verifier and seals the session in a secure cookie", async () => {
    const { location, verifiers } = await startSignIn("/");
    const [name, verifier] = verifiers[0] ?? [];
    const state = location.searchParams.get("state") ?? "";

    const response = await finishSignIn(state, `${name}=${verifier?.value}`);

    const exchange = requests.find(
      ({ url }) => url === "/user_management/authenticate",
    );
    expect(JSON.parse(exchange?.body ?? "{}")).toMatchObject({
      client_id: CLIENT_ID,
      code: "code-1",
      grant_type: "authorization_code",
      code_verifier: expect.any(String) as unknown,
    });
    const session = store.cookies.get("wos-session");
    expect(session?.options).toMatchObject({
      httpOnly: true,
      secure: true,
      sameSite: "lax",
      path: "/",
    });
    expect(session?.value).toMatch(/^Fe26\.2\*/);
    expect(session?.value).not.toContain(issuedToken);
    expect(session?.value).not.toContain("refresh-token-1");
    expect(session?.value).not.toContain(EMAIL);
    expect(response.headers.getSetCookie()).toContainEqual(
      expect.stringMatching(new RegExp(`^${name}=;.*Max-Age=0`)) as unknown,
    );
    expect(response.headers.get("location")).not.toContain("code-1");
  });

  it("rejects a callback whose state does not match the verifier cookie, without contacting WorkOS", async () => {
    const first = await startSignIn("/");
    const second = await startSignIn("/");
    const [name, verifier] = first.verifiers[0] ?? [];
    const otherState = second.location.searchParams.get("state") ?? "";
    requests.length = 0;
    vi.spyOn(console, "error").mockImplementation(() => undefined);

    const response = await finishSignIn(
      otherState,
      `${name}=${verifier?.value}`,
    );

    expect(response.status).toBe(400);
    expect(store.cookies.has("wos-session")).toBe(false);
    expect(requests).toEqual([]);
  });

  it("rejects a callback without a verifier cookie", async () => {
    const { location } = await startSignIn("/");
    vi.spyOn(console, "error").mockImplementation(() => undefined);

    const response = await finishSignIn(
      location.searchParams.get("state") ?? "",
      "other=1",
    );

    expect(response.status).toBe(400);
    expect(requests).toEqual([]);
  });
});
