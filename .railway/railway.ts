import { bucket, defineRailway, github, postgres, preserve, project, service, volume } from "railway/iac";

const STAGING_WEB_URL = "https://civic-scanner-git-staging-andrewcgraves-projects.vercel.app";

export default defineRailway((ctx) => {
  const production = ctx.isEnvironment("production");

  const db = postgres("Postgres", { region: "sfo" });
  db.networking = { privateNetworkEndpoint: "postgres" };

  const dbVolume = volume("postgres-volume", {
    alerts: { usage: { "100": {}, "80": {}, "95": {} } },
    allowOnlineResize: true,
    region: "sfo",
    sizeMB: production ? 50000 : 5000,
  });

  const audio = bucket("audio", { region: "sjc" });

  const api = service("civic-scanner", {
    source: github("andrewcgraves/civic-scanner", {
      branch: "main",
      rootDirectory: "/api",
      checkSuites: true,
    }),
    build: {
      buildEnvironment: "V3",
      watchPatterns: ["/api/**"],
    },
    preDeploy: "/app/migrate up",
    healthcheck: "/v1/healthz",
    healthcheckTimeout: 60,
    deploy: {
      preDeployTimeoutSeconds: 300,
      restartPolicyType: "ON_FAILURE",
      restartPolicyMaxRetries: 5,
    },
    replicas: { sfo: 1 },
    env: {
      DATABASE_URL: db.env.DATABASE_URL,
      APP_ENV: production ? "production" : "staging",
      LOG_LEVEL: "info",
      PORT: "8080",
      STRIPE_SECRET_KEY: preserve(),
      STRIPE_WEBHOOK_SECRET: preserve(),
      WEBHOOK_SECRET_KEY: preserve(),
      ...(production
        ? { POSTHOG_API_KEY: preserve() }
        : {
            FEATURE_FLAGS: "*",
            STRIPE_PRICE_ID: preserve(),
            WORKOS_CLIENT_ID: preserve(),
            PUBLIC_WEB_URL: STAGING_WEB_URL,
          }),
      STORAGE_ENDPOINT: "${{audio.ENDPOINT}}",
      STORAGE_REGION: "${{audio.REGION}}",
      STORAGE_BUCKET: "${{audio.BUCKET}}",
      STORAGE_ACCESS_KEY_ID: "${{audio.ACCESS_KEY_ID}}",
      STORAGE_SECRET_ACCESS_KEY: "${{audio.SECRET_ACCESS_KEY}}",
    },
  });

  const worker = service("civic-scanner-worker", {
    source: github("andrewcgraves/civic-scanner", {
      branch: "main",
      rootDirectory: "/api",
      checkSuites: true,
    }),
    build: {
      builder: "DOCKERFILE",
      buildEnvironment: "V3",
      dockerfilePath: "Dockerfile.worker",
      watchPatterns: ["/api/**"],
    },
    preDeploy: "/app/migrate up",
    deploy: {
      preDeployTimeoutSeconds: 300,
      drainingSeconds: 30,
      restartPolicyType: "ON_FAILURE",
      restartPolicyMaxRetries: 5,
    },
    replicas: { sfo: 1 },
    env: {
      DATABASE_URL: db.env.DATABASE_URL,
      APP_ENV: production ? "production" : "staging",
      LOG_LEVEL: "info",
      AUDIO_MEDIA_USER_AGENT:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
      LLM_PROVIDER: "anthropic",
      TRANSCRIPTION_CONCURRENCY: "1",
      TRANSCRIPTION_CHUNK_RETRIES: "10",
      ...(production
        ? {
            POSTHOG_API_KEY: preserve(),
            ANTHROPIC_API_KEY: preserve(),
            AI_GATEWAY_API_KEY: preserve(),
            FIRECRAWL_API_KEY: preserve(),
          }
        : { FEATURE_FLAGS: "*", INGEST_SCHEDULE: "off" }),
      STORAGE_ENDPOINT: "${{audio.ENDPOINT}}",
      STORAGE_REGION: "${{audio.REGION}}",
      STORAGE_BUCKET: "${{audio.BUCKET}}",
      STORAGE_ACCESS_KEY_ID: "${{audio.ACCESS_KEY_ID}}",
      STORAGE_SECRET_ACCESS_KEY: "${{audio.SECRET_ACCESS_KEY}}",
    },
  });

  return project("reliable-flow", {
    variables: { managed: true },
    resources: [api, worker, db, dbVolume, audio],
  });
});
