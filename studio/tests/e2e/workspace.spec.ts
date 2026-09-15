import { expect, test } from "@playwright/test";

/**
 * Browser smoke over the real stack: Studio's production build in external
 * mode → the /api/mecatl proxy → the fixture daemon (tests/e2e/
 * fixture-daemon.mjs). Assertions are read-only renders of fixture content;
 * the streaming/approval mechanics are covered by the protocol unit suite
 * and the hermetic server-tier suite.
 */

test("the chat list and transcript come from the daemon", async ({ page }) => {
  await page.goto("/workspace/chat");
  // The sidebar row is the daemon's session inventory.
  await page.getByText("Fix the flaky scheduler test").first().click();
  // Opening the chat rehydrates the authoritative transcript.
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
});

test("schedules render the registry with humanized triggers", async ({
  page,
}) => {
  await page.goto("/workspace/schedules");
  // The responsive tables render each row twice (desktop columns + the
  // CSS-collapsed mobile cell), so scope to the visible instance.
  await expect(
    page.getByText("nightly-fixture-digest").filter({ visible: true }),
  ).toBeVisible();
  // The fixture's cron is "0 9 * * *" — the table renders it in plain English.
  await expect(
    page.getByText("Daily at", { exact: false }).first(),
  ).toBeVisible();
});

test("skills render the resolved inventory", async ({ page }) => {
  await page.goto("/workspace/skills");
  await expect(
    page
      .getByText("Review a diff for correctness.", { exact: false })
      .filter({ visible: true }),
  ).toBeVisible();
});

test("memory renders the user model, read-only", async ({ page }) => {
  await page.goto("/workspace/settings/memory");
  await expect(
    page
      .getByText("prefers tabs over spaces", { exact: false })
      .filter({ visible: true }),
  ).toBeVisible();
});

test("external mode marks runtime settings as deployment-owned", async ({
  page,
}) => {
  // Settings is subpages now; the runtime sections live under their own
  // routes, so the assertion targets the provider page directly.
  await page.goto("/workspace/settings/provider");
  await expect(
    page.getByText("Managed by the external mecated deployment").first(),
  ).toBeVisible();
});
