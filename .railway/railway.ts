import { bucket, defineRailway, github, postgres, preserve, project, service, volume } from "railway/iac";

export default defineRailway(() => {
  const db = postgres("Postgres", { region: "sfo" });
  db.networking = { privateNetworkEndpoint: "postgres" };

  const dbVolume = volume("postgres-volume", {
    alerts: { usage: { "100": {}, "80": {}, "95": {} } },
    allowOnlineResize: true,
    region: "sfo",
    sizeMB: 50000,
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
      APP_ENV: "production",
      LOG_LEVEL: "info",
      PORT: "8080",
      STRIPE_SECRET_KEY: preserve(),
      STRIPE_WEBHOOK_SECRET: preserve(),
      POSTHOG_API_KEY: preserve(),
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
      APP_ENV: "production",
      LOG_LEVEL: "info",
      FEATURE_FLAGS: "ingest-check-agencies,ingest-summarize-agendas",
      POSTHOG_API_KEY: preserve(),
      AUDIO_MEDIA_USER_AGENT:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
      LLM_PROVIDER: "anthropic",
      TRANSCRIPTION_CONCURRENCY: "1",
      TRANSCRIPTION_CHUNK_RETRIES: "10",
      ANTHROPIC_API_KEY: preserve(),
      AI_GATEWAY_API_KEY: preserve(),
      FIRECRAWL_API_KEY: preserve(),
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
