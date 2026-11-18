import { defineRailway, github, postgres, project, service, volume } from "railway/iac";

export default defineRailway(() => {
  const db = postgres("Postgres", { region: "sfo" });
  db.networking = { privateNetworkEndpoint: "postgres" };

  const dbVolume = volume("postgres-volume", {
    alerts: { usage: { "100": {}, "80": {}, "95": {} } },
    allowOnlineResize: true,
    region: "sfo",
    sizeMB: 50000,
  });

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
    },
  });

  return project("reliable-flow", {
    variables: { managed: true },
    resources: [api, db, dbVolume],
  });
});
