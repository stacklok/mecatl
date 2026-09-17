import { defineConfig, devices } from "@playwright/test";

const BASE_URL = process.env.BASE_URL || "http://127.0.0.1:3300";
const FIXTURE_PORT = 8099;

/**
 * E2E runs the production build in EXTERNAL mode against the fixture daemon
 * (tests/e2e/fixture-daemon.mjs): browser → real proxy tier → fixture wire.
 * `npm run test:e2e` builds first.
 */
export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? "github" : "list",
  timeout: 30_000,
  use: {
    baseURL: BASE_URL,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  webServer: [
    {
      command: `node tests/e2e/fixture-daemon.mjs`,
      url: `http://127.0.0.1:${FIXTURE_PORT}/v1/models`,
      timeout: 15_000,
      env: { FIXTURE_DAEMON_PORT: String(FIXTURE_PORT) },
    },
    {
      command: `npx next start -p ${new URL(BASE_URL).port}`,
      url: BASE_URL,
      timeout: 120_000,
      env: {
        MECATL_BASE_URL: `http://127.0.0.1:${FIXTURE_PORT}`,
        MECATL_WORKSPACE: "/workspace/fixture",
        MECATL_STUDIO_PUBLIC_ORIGIN: BASE_URL,
      },
    },
  ],
});
