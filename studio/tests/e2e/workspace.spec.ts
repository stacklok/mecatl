import { expect, test } from "@playwright/test";

/**
 * Browser smoke over the real stack: Studio's production build in external
 * mode → the /api/mecatl proxy → the fixture daemon (tests/e2e/
 * fixture-daemon.mjs). Assertions are mostly read-only renders of fixture
 * content; the prompts that DO stream here (the authorization takeover, the
 * subagent lifecycle) exercise the fixture's SSE relay end to end, while the
 * approval mechanics stay with the protocol unit suite and the hermetic
 * server-tier suite.
 */

test("the chat list and transcript come from the daemon", async ({ page }) => {
  await page.goto("/workspace/chat");
  // The draft route's tab names the draft, then the app (no chat open yet).
  await expect(page).toHaveTitle(/^New chat — Mecatl Studio$/);
  // The sidebar row is the daemon's session inventory (by role: the draft's
  // Continue chip names the same chat, so a bare text match is ambiguous).
  await page
    .getByRole("button", { name: /^Open chat: Fix the flaky scheduler test/ })
    .click();
  // Opening the chat rehydrates the authoritative transcript.
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // The tab title follows the open chat (mecatui's window title): the title
  // leads, the app name trails; the fixture chat is idle, so no phase word.
  await expect(page).toHaveTitle(
    /^Fix the flaky scheduler test — Mecatl Studio$/,
  );
});

test("the live chat's model pill shows the daemon's effective reasoning-effort tier", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // The composer's model + effort trigger reads "{model} · {effort}" from
  // the snapshot's resolved_model (the fixture echoes reasoning_effort
  // "medium"); the ContextMeter needs counted tokens and is not the oracle.
  await expect(
    page.locator('button[title="fixture-model · Medium"]').first(),
  ).toBeVisible();
});

test("a tool call parked on a browser sign-in shows the authorization card and re-check resumes the run", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // The fixture parks any prompt that mentions authorization: the stream
  // ends on authorization.required with no result, and the takeover card
  // replaces the composer.
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await composer.click();
  await page.keyboard.type("Run the Fixture MCP authorization flow");
  await page.getByRole("button", { name: "Send message" }).first().click();
  await expect(
    page.getByRole("heading", { name: "Browser authorization required" }),
  ).toBeVisible();
  await expect(
    page.getByText("Fixture MCP needs you to sign in", { exact: false }),
  ).toBeVisible();
  // Re-check streams the continuation run (bodyless POST — the fixture 400s
  // any body byte, like the daemon) and the card gives the composer back.
  await page.getByRole("button", { name: "I've finished — re-check" }).click();
  await expect(
    page.getByText("Authorized: continuing from the fixture.", {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Browser authorization required" }),
  ).toBeHidden();
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

test("schedules show fire count and last run, and the text filter narrows the list", async ({
  page,
}) => {
  await page.goto("/workspace/schedules");
  // The fixture state carries fire_count 3 and a last_fire_at; both land as
  // columns on the desktop table (the mobile cell is CSS-hidden here).
  await expect(page.getByRole("button", { name: "Runs" })).toBeVisible();
  await expect(
    page.getByRole("cell", { name: "3", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Last run" })).toBeVisible();
  await expect(page.getByRole("cell", { name: /^\d+d ago$/ })).toBeVisible();

  // A query nothing matches names itself in the empty state.
  await page.getByPlaceholder("Filter by name or schedule").fill("zzz");
  await expect(page.getByText("No scheduled tasks match “zzz”.")).toBeVisible();
  await expect(page.getByRole("table")).toHaveCount(0);
  await page.getByRole("button", { name: "Clear filter" }).click();
  await expect(
    page.getByText("nightly-fixture-digest").filter({ visible: true }),
  ).toBeVisible();
});

test("the new scheduled task form offers write access, off by default", async ({
  page,
}) => {
  await page.goto("/workspace/schedules");
  await page.getByRole("button", { name: "New scheduled task" }).click();
  const dialog = page.getByRole("dialog");
  // The TUI form's y/n mutating toggle: a labelled switch, read-only until
  // opted in, which then reveals the permission-mode picker.
  const writes = dialog.getByRole("switch", {
    name: "Allow file and shell writes",
  });
  await expect(writes).toBeVisible();
  await expect(writes).not.toBeChecked();
  await expect(
    dialog.getByText("Read-only: the task runs in plan mode", { exact: false }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("combobox", { name: "Permission mode" }),
  ).toHaveCount(0);

  await writes.click();
  await expect(writes).toBeChecked();
  await expect(
    dialog.getByRole("combobox", { name: "Permission mode" }),
  ).toBeVisible();
});

test("the new scheduled task form compiles a natural-language phrase", async ({
  page,
}) => {
  await page.goto("/workspace/schedules");
  await page.getByRole("button", { name: "New scheduled task" }).click();
  const dialog = page.getByRole("dialog");
  // The TUI Create form's phrase input: the phrase compiles into the cron the
  // builder then shows as an interval, with a plain-English preview.
  await dialog.getByLabel("Describe the schedule").fill("every 30 minutes");
  await expect(dialog.getByText("Every 30 minutes")).toBeVisible();
  await expect(dialog.getByRole("combobox", { name: "Repeat" })).toHaveText(
    "Every…",
  );
  await expect(dialog.getByRole("spinbutton", { name: "Every" })).toHaveValue(
    "30",
  );
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
  // The footprint line is the count only.
  await expect(page.getByTestId("memory-footprint")).toHaveText(
    "1 fact remembered",
  );
});

test("memory detail shows the fact value and history", async ({ page }) => {
  // The detail page performs the key-scoped read (`?key=prefers-tabs`), so
  // the fact's VALUE, its daemon-derived provenance and the bounded revision
  // list render — not just the index description.
  await page.goto("/workspace/memory/prefers-tabs");
  await expect(
    page.getByText("Tabs, width 4").filter({ visible: true }),
  ).toBeVisible();
  await expect(page.getByText("reflection", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("link", { name: "session-fixture-1" }),
  ).toHaveAttribute("href", "/workspace/chat/session-fixture-1");
  await expect(page.getByText("1 bounded revision")).toBeVisible();
  await expect(
    page.getByText("2 · superseded", { exact: false }),
  ).toBeVisible();
  await expect(page.getByText("Learned in conversation")).toHaveCount(0);
});

test("learning queue shows evidence provenance", async ({ page }) => {
  await page.goto("/workspace/settings/learning");
  // The Pending pill lists the staged fact; its Details disclosure shows
  // the chat the evidence came from and that it still resolves.
  const row = page.locator("li[data-proposal-id='prop-1']");
  await expect(row).toContainText("scheduler/flaky-test");
  await expect(row.getByRole("button", { name: "Approve" })).toBeEnabled();
  await row.getByRole("button", { name: "Details" }).click();
  const details = row.getByTestId("proposal-details");
  await expect(details.getByText("Available", { exact: true })).toBeVisible();
  await expect(
    details.getByRole("link", { name: "View chat" }),
  ).toHaveAttribute("href", "/workspace/chat/session-fixture-1");
  await expect(details).toContainText("go test ./internal/scheduler");
  // The Deferred pill requests that status: a procedure whose evidence is
  // gone — Approve is offered but disabled with the reason, and there is no
  // Reject (the daemon refuses it outside staged).
  await page.getByRole("button", { name: "Deferred" }).click();
  const deferred = page.locator("li[data-proposal-id='prop-2']");
  await expect(deferred).toContainText("Tag a release");
  await expect(
    deferred.getByRole("button", { name: "Approve" }),
  ).toBeDisabled();
  await expect(deferred).toContainText("no longer available");
  await expect(deferred.getByRole("button", { name: "Reject" })).toHaveCount(0);
  await expect(row).toHaveCount(0);
});

test("external mode marks runtime settings as deployment-owned", async ({
  page,
}) => {
  // Settings is subpages now; the runtime sections live under their own
  // routes, so the assertion targets the provider page directly.
  await page.goto("/workspace/settings/provider");
  await expect(
    page.getByText("The agent is run somewhere else").first(),
  ).toBeVisible();
  // The daemon's own provider_status rows still render read-only in
  // external mode; the fixture's row is state=ok, so it reads Ready and
  // carries NO hint line.
  const fixtureStatus = page.locator("li[data-provider-status='fixture']");
  await expect(fixtureStatus).toBeVisible();
  await expect(fixtureStatus).toContainText("Ready");
  await expect(fixtureStatus.locator("[data-role='hint']")).toHaveCount(0);
});

test("the help reference reflects the daemon's features", async ({ page }) => {
  await page.goto("/workspace/shortcuts");
  await expect(page.getByText("Features on this daemon")).toBeVisible();
  // The fixture advertises steer (capability + the http_steer feature) but
  // not image, so one row is plain and the other carries the tag.
  const steer = page.locator("li[data-feature='steer']");
  await expect(steer).toBeVisible();
  await expect(steer).toHaveAttribute("data-enabled", "true");
  await expect(steer.getByText("not enabled")).toHaveCount(0);
  const image = page.locator("li[data-feature='image']");
  await expect(image.getByText("not enabled")).toBeVisible();
});

test("typing /help in the composer opens the help reference", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await composer.click();
  await page.keyboard.type("/help");
  // The `/` menu lists Studio's builtin; ONE Enter picks it and navigates
  // (no chip is inserted, nothing reaches the daemon).
  await expect(page.getByText("show keys & features")).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/workspace\/shortcuts$/);
  await expect(page.getByText("Features on this daemon")).toBeVisible();
});

test("the / palette lists Studio's built-ins ahead of the daemon's commands and /clear opens the successor chat", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await composer.click();
  await page.keyboard.type("/");
  // The client-owned layer comes first, in the TUI's fixed order; the
  // fixture daemon advertises manual compaction, so /compact is offered.
  const rows = page.locator("button", { hasText: /^\// });
  await expect(rows.first()).toContainText("/clear");
  await expect(page.getByText("clear the conversation")).toBeVisible();
  await expect(
    page.getByText("compact this session's model history"),
  ).toBeVisible();
  // Typing the rest and pressing Enter runs the built-in locally: the
  // ClearSession successor is minted and the UI moves to it; nothing is
  // sent as a prompt.
  await page.keyboard.type("clear");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-clear$/);
  await expect(
    page.getByText(
      "Conversation cleared — continuing in a fresh chat with the same settings",
    ),
  ).toBeVisible();
});

test("Clear conversation in the chat menu mints a successor and re-enables the composer", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // The fixture row offers `fork`, so the item is enabled (the same daemon
  // verdict that gates the model/effort switch).
  await page.getByRole("button", { name: "Chat options" }).click();
  await page
    .getByRole("menuitem", { name: "Clear conversation", exact: true })
    .click();
  // The scrollback switches only after the daemon answered with the
  // successor; the old chat stays in the list and the composer is usable.
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-clear$/);
  await expect(
    page.getByText(
      "Conversation cleared — continuing in a fresh chat with the same settings",
    ),
  ).toBeVisible();
  await expect(
    page.getByText("Fix the flaky scheduler test").first(),
  ).toBeVisible();
  await expect(
    page.locator(".composer-editor .ProseMirror").first(),
  ).toHaveAttribute("contenteditable", "true");
});

test("Switch worktree… lists the fixture worktrees and moves to the picked one", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // Gated on the fixture's `worktrees` capability AND the row's `fork`
  // verdict (the same daemon verdict that gates Clear conversation).
  await page.getByRole("button", { name: "Chat options" }).click();
  await page
    .getByRole("menuitem", { name: "Switch worktree…", exact: true })
    .click();
  const dialog = page.getByRole("dialog", { name: "Switch worktree" });
  // The two fixture worktrees, in daemon order, with branch and short rev.
  const main = dialog.getByRole("radio", { name: /^main main/ });
  const feature = dialog.getByRole("radio", { name: /feature-x/ });
  await expect(main).toBeVisible();
  await expect(feature).toBeVisible();
  await expect(dialog.getByText("feature/x")).toBeVisible();
  await expect(dialog.getByText("0123456")).toBeVisible();
  // Nothing picked: Switch stays disabled until a worktree is chosen.
  const confirm = dialog.getByRole("button", { name: "Switch", exact: true });
  await expect(confirm).toBeDisabled();
  await feature.check();
  await confirm.click();
  // The default is a fresh (clear) successor: the fixture's clear route
  // answers the cleared id and the UI moves there; the old chat stays.
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-clear$/);
  await expect(
    page.getByText("Now working in feature-x (feature/x)"),
  ).toBeVisible();
  await expect(
    page.getByText("Fix the flaky scheduler test").first(),
  ).toBeVisible();
});

test("Debug with AI in the row menu opens the consent dialog naming the chat, its reporting servers and the opening message", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  // The row menu's item is gated on the fixture's `session_debug`; its
  // options button reveals on hover (≥500px), so hover the row first.
  const row = page.getByRole("button", {
    name: /^Open chat: Fix the flaky scheduler test/,
  });
  await row.hover();
  await page
    .getByRole("button", {
      name: "Options for chat: Fix the flaky scheduler test",
    })
    .click();
  await page
    .getByRole("menuitem", { name: "Debug with AI", exact: true })
    .click();
  // The consent names the target chat; the attach section lists the daemon's
  // configured server (GET /v1/mcp/sources, gated on `debug_mcp`), unpicked.
  const dialog = page.getByRole("dialog", {
    name: "Debug with AI: Fix the flaky scheduler test",
  });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByText("will be sent to the model as debugging evidence", {
      exact: false,
    }),
  ).toBeVisible();
  const server = dialog.getByRole("checkbox", { name: "fixture-mcp" });
  await expect(server).toBeVisible();
  await expect(server).not.toBeChecked();
  // The runtime-context report is on by default, and the opening message the
  // debug chat will submit on its own is shown before it leaves.
  await expect(
    dialog.getByRole("switch", {
      name: "Include a Studio/daemon diagnostics report as runtime context",
    }),
  ).toBeChecked();
  await expect(
    dialog.getByText("Diagnose the bound target session", { exact: false }),
  ).toBeVisible();
  // Picking the server re-spells the message with the TUI's suffix.
  await server.check();
  await expect(
    dialog.getByText("Selected reporting servers are available: fixture-mcp.", {
      exact: false,
    }),
  ).toBeVisible();
  // Nothing was created: Cancel closes without a daemon call.
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
});

test("a scheduled task's delivery note renders as an attributed card", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  // The fixture transcript's third message is the daemon's fenced note.
  const card = page.getByRole("article", {
    name: "Scheduled task nightly-fixture-digest",
  });
  await expect(card).toBeVisible();
  await expect(
    card.getByRole("link", { name: "nightly-fixture-digest" }),
  ).toHaveAttribute("href", "/workspace/schedules/nightly-fixture-digest");
  await expect(card.getByText("fire fire-1")).toBeVisible();
  await expect(card.getByText("completed")).toBeVisible();
  await expect(
    card.getByText("Digest: 3 PRs merged", { exact: false }),
  ).toBeVisible();
  // The fence and the header are machine markers for the model, never shown.
  await expect(page.getByText("<<<UNTRUSTED")).toHaveCount(0);
  await expect(page.getByText("[scheduled task", { exact: false })).toHaveCount(
    0,
  );
});

test("storage settings show aggregate health and the clean-up plan", async ({
  page,
}) => {
  // The fixture advertises storage_health / storage_cleanup, so the Storage
  // card renders: the health summary off GET /v1/storage/health and the
  // find old runs → typed CLEAN UP → apply flow over its static jobs.
  await page.goto("/workspace/settings/storage");
  await expect(page.getByTestId("storage-health-status")).toHaveText("Healthy");
  await expect(page.getByTestId("storage-health-sessions")).toContainText("3");
  await expect(page.getByTestId("storage-health-size")).toHaveText("20 KB");

  await page
    .getByRole("button", { name: "Find old runs", exact: true })
    .click();
  await expect(page.getByTestId("storage-cleanup-eligible")).toContainText("1");
  await expect(page.getByTestId("storage-cleanup-protected")).toContainText(
    "2",
  );
  await page.getByRole("button", { name: "Clean up…", exact: true }).click();
  const dialog = page.getByRole("alertdialog");
  const confirm = dialog.getByRole("button", { name: "Clean up", exact: true });
  await expect(confirm).toBeDisabled();
  await dialog.getByRole("textbox").fill("clean up");
  await expect(confirm).toBeDisabled();
  await dialog.getByRole("textbox").fill("CLEAN UP");
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect(page.getByTestId("storage-cleanup-job-state")).toHaveText(
    "Completed",
  );
  await expect(page.getByTestId("storage-cleanup-progress")).toContainText(
    "1 deleted",
  );
});

test("a prompt's subagent lifecycle feeds the inline card, the fleet chip, and the Agents panel", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // No child has run in this chat yet, so the composer strip has no chip.
  await expect(
    page.getByRole("list", { name: "Agents in this chat" }),
  ).toHaveCount(0);
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await composer.click();
  await page.keyboard.type("Scan the scheduler tests for me");
  await page.getByRole("button", { name: "Send message" }).first().click();
  // The fixture streams subagent.start → tool → end inside the turn: the
  // turn's delegation card names the child, and the persistent chip beside
  // the context meter tallies it once the end frame lands.
  await expect(
    page.getByText("subagent: Scan the scheduler tests", { exact: false }),
  ).toBeVisible();
  const chip = page.getByRole("button", {
    name: "subagents · 0 running · 1 done",
  });
  await expect(chip).toBeVisible();
  // The chip opens the Agents panel on its family's tab.
  await chip.click();
  await expect(
    page.getByText("subagents · 0 running · 1 done", { exact: true }),
  ).toBeVisible();
});

test("Session details opens the full dialog with the resolved model and safety level", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await page.getByRole("button", { name: "Chat options" }).click();
  await page.getByRole("menuitem", { name: "Session details" }).click();
  const dialog = page.getByRole("dialog");
  // The daemon-RESOLVED model off the snapshot's resolved_model (the fixture
  // lists it as "fixture-model").
  await expect(dialog.getByText("fixture-model")).toBeVisible();
  // The fixture's compatibility document reports posture "trusted"; E2E runs
  // against an external daemon.
  await expect(dialog.getByTestId("session-details-posture")).toHaveText(
    "Trusted",
  );
  await expect(dialog.getByTestId("session-details-server")).toContainText(
    "External deployment",
  );
});

test("the workspace-services notice connects through the daemon and clears", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  // The fixture advertises workspace_enrollment and its connector inventory
  // reads not_started: the TUI's "workspace services not connected" notice
  // shows above the composer with the /tools-connect action as a button.
  const notice = page.getByTestId("workspace-enrollment-notice");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("Workspace services aren't connected");
  // Connect opens the consent window on the click, then POSTs the bodyless
  // connect; the fixture answers "connected" outright, so the window closes
  // again, the notice clears and the toast confirms.
  await notice.getByRole("button", { name: "Connect" }).click();
  await expect(page.getByText("Workspace services connected")).toBeVisible();
  await expect(notice).toHaveCount(0);
});

test("the Permissions page reads the daemon-reported posture in external mode and offers no launch flags", async ({
  page,
}) => {
  // Posture, project trust and shell-less mode are spawn flags of the
  // MANAGED daemon; the external deployment owns its own. So the page shows
  // only the EFFECTIVE tier off the fixture's compatibility document
  // (`posture: "trusted"`) and the managed note — no tier picker, no trust
  // switch, nothing that would pretend to change a flag Studio cannot pass.
  await page.goto("/workspace/settings/permissions");
  const effectiveRow = page
    .getByText("Safety level", { exact: true })
    .locator("xpath=ancestor::div[2]");
  await expect(
    effectiveRow.getByText("Trusted", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("The agent is run somewhere else").first(),
  ).toBeVisible();
  await expect(page.getByRole("switch")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Safety level" })).toHaveCount(
    0,
  );
});

test("View transcript in the row menu opens the read-only dialog without opening the chat", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  // The row's options button reveals on hover (≥500px), so hover the row
  // first. The fixture row offers `view_transcript`, so the item is enabled.
  const row = page.getByRole("button", {
    name: /^Open chat: Fix the flaky scheduler test/,
  });
  await row.hover();
  await page
    .getByRole("button", {
      name: "Options for chat: Fix the flaky scheduler test",
    })
    .click();
  await page
    .getByRole("menuitem", { name: "View transcript", exact: true })
    .click();
  // The read-only transcript dialog, labelled by the chat title,
  // replaying the daemon's authoritative transcript.
  const dialog = page.getByRole("dialog", {
    name: "Fix the flaky scheduler test",
  });
  await expect(
    dialog.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // Read-only: the chat did not become the live chat — the URL is still the
  // draft route and the tab title unchanged.
  await expect(page).toHaveURL(/\/workspace\/chat$/);
  await expect(page).toHaveTitle(/^New chat — Mecatl Studio$/);
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
});

test("Fork chat in the row menu continues in a copy of the chat", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  const row = page.getByRole("button", {
    name: /^Open chat: Fix the flaky scheduler test/,
  });
  await row.hover();
  await page
    .getByRole("button", {
      name: "Options for chat: Fix the flaky scheduler test",
    })
    .click();
  // The fixture row offers `fork` (the same daemon verdict that gates Clear
  // conversation and the model/effort switch), so the item is enabled.
  await page.getByRole("menuitem", { name: "Fork chat", exact: true }).click();
  // The UI moves to the copy only after the daemon answered with its id; the
  // copy carries the source's conversation and the source stays in the list.
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-fork$/);
  await expect(
    page.getByText("Forked — continuing in a copy of this chat"),
  ).toBeVisible();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByText("Fix the flaky scheduler test").first(),
  ).toBeVisible();
});

test("Settings → About shows Studio's version and the docs link", async ({
  page,
}) => {
  await page.goto("/workspace/settings/help");
  // The web `--version`: Studio's own version, inlined at `next build`, so
  // it is a real value before any daemon call.
  await expect(page.getByTestId("about-studio-version")).toHaveText(
    /^\d+\.\d+\.\d+/,
  );
  await expect(
    page.getByRole("link", { name: "Documentation" }),
  ).toHaveAttribute("href", "https://mecatl.dev/docs/");
  await expect(
    page.getByRole("link", { name: "Keyboard shortcuts" }),
  ).toHaveAttribute("href", "/workspace/shortcuts");
  // The agent's version card shares the page (the fixture answers /v1/info).
  await expect(page.getByTestId("about-server-build")).toHaveText("fixture");
  // No endpoint is rendered anywhere on the page.
  await expect(page.getByText("127.0.0.1:8099")).toHaveCount(0);
});

test("Settings → MCP tools lists the resolved sources with their tools and skipped count", async ({
  page,
}) => {
  await page.goto("/workspace/settings/gateway");
  // The fixture advertises `mcp`: the card lists both sources by a readable
  // name with their tools and a plain skipped count (GET /v1/mcp/sources).
  const card = page.getByRole("heading", { name: "MCP tools" }).locator("..");
  await expect(page.getByText("ToolHive", { exact: true })).toBeVisible();
  await expect(page.getByText("http://127.0.0.1:1/mcp")).toHaveCount(0);
  await expect(page.getByTestId("mcp-skipped")).toHaveText(
    "One tool couldn't be connected.",
  );
  // The footer carries the refresh caveat until a manual refresh lands,
  // then reads up to date.
  await expect(page.getByTestId("mcp-footer")).toContainText(
    "appear after you refresh",
  );
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByTestId("mcp-footer")).toHaveText("Up to date.");
  // The gateway connect form still shares the page below the inventory.
  await expect(card).toBeVisible();
  await expect(page.getByText("MCP gateway", { exact: true })).toBeVisible();
});

test("the chat's MCP tools panel lists the broker connectors with their catalogue state", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  // The header's MCP tools button is gated on the fixture's `mcp` /
  // `mcp_connector_status`; the panel reads the chat's connector inventory
  // (GET /v1/sessions/{id}/mcp/connectors) in the TUI's words, never as a
  // live connection claim, and the sources below it.
  await page.getByRole("button", { name: "MCP tools", exact: true }).click();
  const panel = page.getByTestId("mcp-panel");
  await expect(panel).toBeVisible();
  await expect(panel.getByTestId("mcp-enrollment")).toHaveText(
    "Enrollment: No active setup",
  );
  const connector = panel.getByTestId("mcp-connector");
  await expect(connector).toContainText("fixture-connector");
  await expect(connector).toContainText("Awaiting discovery");
  await expect(connector).toContainText("— tools");
  await expect(
    panel.getByText("Catalogue status · not a live connection check"),
  ).toBeVisible();
  // The whole-bundle setup actions ride `workspace_enrollment`: connect is
  // offered on the idle chat, cancel waits for a pending setup.
  await expect(
    panel.getByRole("button", { name: "Connect tools" }),
  ).toBeEnabled();
  await expect(
    panel.getByRole("button", { name: "Cancel setup" }),
  ).toBeDisabled();
  await expect(panel.getByText("fixture-mcp")).toBeVisible();
  await expect(
    panel.getByRole("link", { name: "Configure in Settings → MCP tools" }),
  ).toHaveAttribute("href", "/workspace/settings/gateway");
});

test("the composer inserts an MCP prompt after filling its argument", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // The composer's "Insert from MCP" entry is gated on the fixture's `mcp`;
  // the prompt picker lists GET /v1/mcp/prompts, holds Render until the
  // required argument is filled, previews POST /v1/mcp/prompts/get's
  // messages, and inserts them — role-prefixed — into the draft. Nothing
  // is sent: no user bubble, no fixture reply.
  await page.getByRole("button", { name: "Insert from MCP" }).first().click();
  await page.getByRole("menuitem", { name: /^Prompt/ }).click();
  const dialog = page.getByRole("dialog", { name: "Insert an MCP prompt" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: /Summarize a file/ }).click();
  const render = dialog.getByRole("button", { name: "Render" });
  await expect(render).toBeDisabled();
  await dialog.getByLabel(/File path/).fill("README.md");
  await expect(render).toBeEnabled();
  await render.click();
  await expect(
    dialog.getByText("Summarize the file at README.md."),
  ).toBeVisible();
  await expect(dialog.getByText("I will read it first.")).toBeVisible();
  await dialog.getByRole("button", { name: "Insert into message" }).click();
  await expect(dialog).toHaveCount(0);
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await expect(composer).toContainText(
    "user: Summarize the file at README.md.",
  );
  await expect(composer).toContainText("assistant: I will read it first.");
  await expect(page.getByText("Streaming from the fixture.")).toHaveCount(0);
});

test("the composer previews and inserts an MCP resource", async ({ page }) => {
  await page.goto("/workspace/chat");
  await page.getByText("Fix the flaky scheduler test").first().click();
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  // The resource picker lists GET /v1/mcp/resources, reads the picked one
  // (GET /v1/mcp/resources/read) into a preview pane, and inserts its text
  // into the draft for review — never sending it.
  await page.getByRole("button", { name: "Insert from MCP" }).first().click();
  await page.getByRole("menuitem", { name: /^Resource/ }).click();
  const dialog = page.getByRole("dialog", { name: "Insert an MCP resource" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: /Fixture notes/ }).click();
  await expect(dialog.getByTestId("mcp-resource-preview")).toHaveText(
    "Fixture notes: the scheduler test is flaky.",
  );
  await dialog.getByRole("button", { name: "Insert into message" }).click();
  await expect(dialog).toHaveCount(0);
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await expect(composer).toContainText(
    "Fixture notes: the scheduler test is flaky.",
  );
  await expect(page.getByText("Streaming from the fixture.")).toHaveCount(0);
});

test("a ?prompt= deep link pre-fills the draft composer, strips the query and sends nothing", async ({
  page,
}) => {
  // The web analogue of `mecatui -p` without auto-send: the text is in the
  // composer, the address bar is clean (a reload would not re-seed), and no
  // turn ran — no confirmation, no user bubble, no fixture reply.
  await page.goto("/workspace/chat?prompt=Hello%20fixture");
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await expect(composer).toHaveText("Hello fixture");
  await expect(page).toHaveURL(/\/workspace\/chat$/);
  await expect(
    page.getByRole("dialog", { name: "Send this prompt?" }),
  ).toHaveCount(0);
  await expect(page.getByText("Streaming from the fixture.")).toHaveCount(0);
});

test("a ?prompt=&send=1 deep link confirms the exact prompt, sends it once and leaves an interactive chat", async ({
  page,
}) => {
  // A URL is drive-by reachable, so `send=1` never sends on its own: the
  // exact text waits behind a one-click confirmation. The query is already
  // stripped while the dialog is up, so a reload could not re-ask.
  await page.goto("/workspace/chat?prompt=Hello%20fixture&send=1");
  const dialog = page.getByRole("dialog", { name: "Send this prompt?" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByTestId("seed-prompt-text")).toHaveText(
    "Hello fixture",
  );
  await expect(page).toHaveURL(/\/workspace\/chat$/);
  await expect(page.getByText("Streaming from the fixture.")).toHaveCount(0);
  // Send takes the composer's own path: the draft mints its daemon session
  // (POST /v1/sessions → session-fixture-1), the prompt streams, and the URL
  // moves to the minted chat.
  await dialog.getByRole("button", { name: "Send", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText("Hello fixture", { exact: true })).toBeVisible();
  await expect(page.getByText("Streaming from the fixture.")).toBeVisible();
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-1$/);
  // Then it is an ordinary chat: the composer is back, empty and enabled.
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await expect(composer).toBeVisible();
  await expect(composer).toHaveText("");
});

test("a ?prompt= deep link on an open chat pre-fills that chat's composer and sends nothing", async ({
  page,
}) => {
  // `/workspace/chat/<id>?prompt=` is the same arrival aimed at a stored
  // chat: its transcript loads, the text waits in ITS composer, the query
  // goes but the path stays, and no turn ran.
  await page.goto("/workspace/chat/session-fixture-1?prompt=Follow%20up");
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  const composer = page.locator(".composer-editor .ProseMirror").first();
  await expect(composer).toHaveText("Follow up");
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-1$/);
  await expect(
    page.getByRole("dialog", { name: "Send this prompt?" }),
  ).toHaveCount(0);
  await expect(page.getByText("Streaming from the fixture.")).toHaveCount(0);
});

test("the Continue chip on the draft opens the most recent chat without any preference", async ({
  page,
}) => {
  await page.goto("/workspace/chat");
  // Default preference: the draft stays, and names the newest quiet chat
  // (the fixture's one idle main chat) as a Continue chip.
  await expect(page).toHaveTitle(/^New chat — Mecatl Studio$/);
  const chip = page.getByRole("button", { name: /^Continue/ });
  await expect(chip).toContainText("Continue “Fix the flaky scheduler test”");
  await chip.click();
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-1$/);
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
});

test("the Most recent chat launch preference opens the newest chat on the bare route, and New chat still gives a draft", async ({
  page,
}) => {
  // Settings → Personalize → Start on: "Most recent chat" is browser-local.
  await page.addInitScript(() => {
    localStorage.setItem("mecatl-studio.launch-target", "latest");
  });
  await page.goto("/workspace/chat");
  // The landing waits for the daemon probe and the inventory (up to one
  // 5 s probe interval), then REPLACES the URL with the newest chat.
  await expect(page).toHaveURL(/\/workspace\/chat\/session-fixture-1$/, {
    timeout: 15_000,
  });
  await expect(
    page.getByText("the test races the claim sentinel", { exact: false }),
  ).toBeVisible();
  await expect(page).toHaveTitle(
    /^Fix the flaky scheduler test — Mecatl Studio$/,
  );
  // An explicit New chat wins over the preference: the draft stays put
  // instead of bouncing straight back to the most recent chat.
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await expect(page).toHaveURL(/\/workspace\/chat$/);
  await expect(
    page.getByRole("heading", { name: "What can I help you with?" }),
  ).toBeVisible();
  // Give a would-be re-open its probe interval and confirm nothing moved.
  await page.waitForTimeout(6_000);
  await expect(page).toHaveURL(/\/workspace\/chat$/);
  await expect(
    page.getByRole("heading", { name: "What can I help you with?" }),
  ).toBeVisible();
});
