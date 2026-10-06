// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "./fixtures";

test("offline Vite app loads through the BFF fixture", async ({ offlineBff, page }) => {
  offlineBff.json("GET", "/api/v1/auth/session", { mode: "oidc", status: "anonymous" });
  offlineBff.json("GET", "/api/v1/status", {
    connection: "reachable",
    signInRequired: true,
  });

  await page.goto("/workspace/chat", { waitUntil: "domcontentloaded" });

  await expect(page.getByRole("main")).toBeVisible();
  await expect
    .poll(() => offlineBff.requestsFor("GET", "/api/v1/auth/session").length)
    .toBeGreaterThan(0);
});
