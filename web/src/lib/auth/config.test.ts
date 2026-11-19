import { afterEach, describe, expect, it, vi } from "vitest";

const VALID = {
  WORKOS_CLIENT_ID: "client_01TEST",
  WORKOS_COOKIE_PASSWORD: "a".repeat(32),
  NEXT_PUBLIC_WORKOS_REDIRECT_URI: "https://civic.example/auth/callback",
};

const UNSET = {
  WORKOS_CLIENT_ID: undefined,
  WORKOS_COOKIE_PASSWORD: undefined,
  NEXT_PUBLIC_WORKOS_REDIRECT_URI: undefined,
};

async function loadConfig(env: Record<string, string | undefined>) {
  for (const [name, value] of Object.entries({ ...UNSET, ...env })) {
    vi.stubEnv(name, value);
  }
  vi.resetModules();
  return import("./config");
}

describe("readAuthConfig", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("is off when no WorkOS variable is set", async () => {
    const { readAuthConfig } = await loadConfig({});

    expect(readAuthConfig()).toEqual({ kind: "off" });
  });

  it("is off when the variables are blank", async () => {
    const { readAuthConfig } = await loadConfig({
      WORKOS_CLIENT_ID: " ",
      WORKOS_COOKIE_PASSWORD: "",
      NEXT_PUBLIC_WORKOS_REDIRECT_URI: " ",
    });

    expect(readAuthConfig()).toEqual({ kind: "off" });
  });

  it("is on with every variable valid", async () => {
    const { readAuthConfig } = await loadConfig(VALID);

    expect(readAuthConfig()).toEqual({
      kind: "on",
      origin: "https://civic.example",
      redirectUri: "https://civic.example/auth/callback",
    });
  });

  it("accepts http on localhost", async () => {
    const { readAuthConfig } = await loadConfig({
      ...VALID,
      NEXT_PUBLIC_WORKOS_REDIRECT_URI: "http://localhost:3000/auth/callback",
    });

    expect(readAuthConfig()).toMatchObject({
      kind: "on",
      origin: "http://localhost:3000",
    });
  });

  it("names every missing variable when only some are set", async () => {
    const { readAuthConfig } = await loadConfig({
      WORKOS_CLIENT_ID: VALID.WORKOS_CLIENT_ID,
    });

    expect(readAuthConfig()).toEqual({
      kind: "invalid",
      problems: [
        "WORKOS_COOKIE_PASSWORD is not set",
        "NEXT_PUBLIC_WORKOS_REDIRECT_URI is not set",
      ],
    });
  });

  it("refuses a missing client id", async () => {
    const { readAuthConfig } = await loadConfig({
      ...VALID,
      WORKOS_CLIENT_ID: undefined,
    });

    expect(readAuthConfig()).toEqual({
      kind: "invalid",
      problems: ["WORKOS_CLIENT_ID is not set"],
    });
  });

  it("refuses a client id that is not one", async () => {
    const { readAuthConfig } = await loadConfig({
      ...VALID,
      WORKOS_CLIENT_ID: "sk_test_secret",
    });

    expect(readAuthConfig()).toEqual({
      kind: "invalid",
      problems: ["WORKOS_CLIENT_ID does not start with client_"],
    });
  });

  it("refuses a cookie password shorter than 32 characters", async () => {
    const { readAuthConfig } = await loadConfig({
      ...VALID,
      WORKOS_COOKIE_PASSWORD: "a".repeat(31),
    });

    expect(readAuthConfig()).toEqual({
      kind: "invalid",
      problems: ["WORKOS_COOKIE_PASSWORD is shorter than 32 characters"],
    });
  });

  it.each([
    ["not a URL", "NEXT_PUBLIC_WORKOS_REDIRECT_URI is not an absolute URL"],
    [
      "http://civic.example/auth/callback",
      "NEXT_PUBLIC_WORKOS_REDIRECT_URI must use https (http only for localhost)",
    ],
    [
      "javascript://civic.example/auth/callback",
      "NEXT_PUBLIC_WORKOS_REDIRECT_URI must use https (http only for localhost)",
    ],
    [
      "https://user:pass@civic.example/auth/callback",
      "NEXT_PUBLIC_WORKOS_REDIRECT_URI must not contain user info",
    ],
    [
      "https://civic.example/auth/callback?next=/",
      "NEXT_PUBLIC_WORKOS_REDIRECT_URI must not have a query or fragment",
    ],
    [
      "https://civic.example/auth/callback#x",
      "NEXT_PUBLIC_WORKOS_REDIRECT_URI must not have a query or fragment",
    ],
    [
      "https://civic.example/callback",
      "NEXT_PUBLIC_WORKOS_REDIRECT_URI must end in /auth/callback",
    ],
  ])("refuses the redirect URI %s", async (redirectUri, problem) => {
    const { readAuthConfig } = await loadConfig({
      ...VALID,
      NEXT_PUBLIC_WORKOS_REDIRECT_URI: redirectUri,
    });

    expect(readAuthConfig()).toEqual({ kind: "invalid", problems: [problem] });
  });
});

describe("hasPublicRedirectUri", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it.each([
    [{}, false],
    [
      { NEXT_PUBLIC_WORKOS_REDIRECT_URI: "http://civic.example/auth/callback" },
      false,
    ],
    [
      {
        NEXT_PUBLIC_WORKOS_REDIRECT_URI: VALID.NEXT_PUBLIC_WORKOS_REDIRECT_URI,
      },
      true,
    ],
    [VALID, true],
  ])("for %j is %s, whatever the secrets", async (env, expected) => {
    const { hasPublicRedirectUri } = await loadConfig(env);

    expect(hasPublicRedirectUri()).toBe(expected);
  });
});

describe("getAuthConfig", () => {
  const LOGGED = Symbol.for("civic-scanner.auth-config-logged");

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
    delete (globalThis as { [LOGGED]?: true })[LOGGED];
  });

  it("logs nothing when sign-in is not configured", async () => {
    const error = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { getAuthConfig } = await loadConfig({});

    expect(getAuthConfig()).toEqual({ kind: "off" });
    expect(error).not.toHaveBeenCalled();
  });

  it("logs the reason once per process, without values", async () => {
    const error = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const secret = "short-secret";
    const first = await loadConfig({
      ...VALID,
      WORKOS_COOKIE_PASSWORD: secret,
    });

    expect(first.getAuthConfig().kind).toBe("invalid");
    first.getAuthConfig();
    const second = await loadConfig({
      ...VALID,
      WORKOS_COOKIE_PASSWORD: secret,
    });
    second.getAuthConfig();

    expect(error).toHaveBeenCalledTimes(1);
    expect(error).toHaveBeenCalledWith(
      "Sign-in is disabled because WorkOS is misconfigured: WORKOS_COOKIE_PASSWORD is shorter than 32 characters",
    );
    expect(JSON.stringify(error.mock.calls)).not.toContain(secret);
  });
});
