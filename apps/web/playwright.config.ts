// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "@playwright/test";

const webRoot = dirname(fileURLToPath(import.meta.url));
const appsRoot = resolve(webRoot, "..");
const repositoryRoot = resolve(webRoot, "../..");
// Playwright loads this config again in each worker, so the port must stay
// stable across processes while differing from another checkout's dev server.
const worktreePort =
  20_000 +
  (Number.parseInt(createHash("sha256").update(webRoot).digest("hex").slice(0, 8), 16) % 30_000);
const port = Number(process.env.STUDIO_BROWSER_PORT ?? worktreePort);
if (!Number.isInteger(port) || port < 1024 || port > 65_535) {
  throw new Error("STUDIO_BROWSER_PORT must be an available TCP port between 1024 and 65535");
}
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  expect: { timeout: 5_000 },
  forbidOnly: Boolean(process.env.CI),
  fullyParallel: false,
  globalTimeout: 300_000,
  outputDir: join(repositoryRoot, ".scratch", "studio-browser-playwright"),
  projects: [
    {
      name: "chromium-desktop",
      use: { browserName: "chromium", viewport: { width: 1280, height: 800 } },
    },
    {
      name: "chromium-mobile",
      use: {
        browserName: "chromium",
        hasTouch: true,
        isMobile: true,
        viewport: { width: 390, height: 844 },
      },
    },
  ],
  reporter: "list",
  retries: 0,
  testDir: join(webRoot, "e2e"),
  testMatch: "*.spec.ts",
  timeout: 60_000,
  use: {
    actionTimeout: 10_000,
    baseURL,
    navigationTimeout: 30_000,
    screenshot: "only-on-failure",
    serviceWorkers: "block",
    trace: "retain-on-failure",
  },
  webServer: {
    command: `pnpm --filter @mecatl-studio/web exec vite --host 127.0.0.1 --port ${port}`,
    cwd: appsRoot,
    reuseExistingServer: false,
    timeout: 90_000,
    url: baseURL,
  },
  workers: 1,
});
