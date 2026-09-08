import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { defineConfig, devices } from "@playwright/test";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");

export default defineConfig({
  expect: { timeout: 5_000 },
  forbidOnly: Boolean(process.env.CI),
  fullyParallel: false,
  globalTimeout: 120_000,
  outputDir: join(repositoryRoot, ".scratch", "sdk-browser-playwright"),
  projects: [
    {
      name: "chromium-full",
      testMatch: "chromium.e2e.test.ts",
      use: {
        ...devices["Desktop Chrome"],
        browserName: "chromium",
      },
    },
    {
      grep: /Firefox imports connects runs and attaches/u,
      name: "firefox-smoke",
      testMatch: "smoke.e2e.test.ts",
      use: {
        ...devices["Desktop Firefox"],
        browserName: "firefox",
      },
    },
    {
      grep: /WebKit imports connects runs and attaches/u,
      name: "webkit-smoke",
      testMatch: "smoke.e2e.test.ts",
      use: {
        ...devices["Desktop Safari"],
        browserName: "webkit",
      },
    },
  ],
  reporter: "list",
  retries: 0,
  testDir: ".",
  timeout: 30_000,
  use: {
    actionTimeout: 10_000,
    navigationTimeout: 10_000,
    serviceWorkers: "block",
    trace: "retain-on-failure",
  },
  workers: 1,
});
