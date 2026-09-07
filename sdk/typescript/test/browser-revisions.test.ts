import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { expect, it } from "vitest";

interface BrowserRecord {
  readonly browserVersion: string;
  readonly revision: string;
}

interface RecordedRevisions {
  readonly chromium: BrowserRecord;
  readonly firefox: BrowserRecord;
  readonly playwright: string;
  readonly webkit: BrowserRecord;
}

interface PlaywrightManifest {
  readonly browsers: ReadonlyArray<{
    readonly browserVersion?: string;
    readonly name: string;
    readonly revision: string;
  }>;
}

it("records the pinned Playwright engine revisions", async () => {
  const require = createRequire(import.meta.url);
  const playwrightPackagePath = require.resolve("@playwright/test/package.json");
  const playwrightPackage = JSON.parse(await readFile(playwrightPackagePath, "utf8")) as {
    version: string;
  };
  const playwrightRequire = createRequire(playwrightPackagePath);
  const corePackagePath = playwrightRequire.resolve("playwright-core/package.json");
  const manifest = JSON.parse(
    await readFile(join(dirname(corePackagePath), "browsers.json"), "utf8"),
  ) as PlaywrightManifest;
  const testDirectory = dirname(fileURLToPath(import.meta.url));
  const recorded = JSON.parse(
    await readFile(join(testDirectory, "..", "e2e", "browser", "tested-revisions.json"), "utf8"),
  ) as RecordedRevisions;

  expect(recorded.playwright).toBe(playwrightPackage.version);
  for (const name of ["chromium", "firefox", "webkit"] as const) {
    const installed = manifest.browsers.find((browser) => browser.name === name);
    expect(installed, `${name} is absent from Playwright's browser manifest`).toMatchObject(
      recorded[name],
    );
  }
});
