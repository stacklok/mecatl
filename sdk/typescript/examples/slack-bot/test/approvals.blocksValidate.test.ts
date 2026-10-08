import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { PermissionApprovalGateway } from "../src/approvals.js";
import { BLOCKS_VALIDATE_CASES } from "./fixtures/blocksValidateCases.js";
import { fakeAsk, fakeClient, fakeContext, flush } from "./helpers.js";

interface CachedCase {
  id: string;
  blocks: unknown;
  result: { ok: boolean; errors?: unknown };
}

/**
 * Cross-checks the blocks `PermissionApprovalGateway` builds TODAY against a cache of real
 * Slack `blocks.validate` results (fixtures/blocksValidateCache.json) - genuine ground truth,
 * recorded by test/record-blocks-validate-cache.ts, without hitting the network on every run.
 *
 * This is sound only because it fails loudly on drift, never silently: if today's code renders
 * a case's blocks differently than what's cached, that's NOT "probably still fine" - it's a
 * hard failure telling you to re-run the recorder (see its header comment) so a live check
 * decides whether the new shape is still Slack-valid, rather than this test quietly trusting an
 * unvalidated new output. A cache entry can only ever prove ONE exact JSON shape was valid on
 * the day it was recorded.
 *
 * To add new coverage: add a case to fixtures/blocksValidateCases.ts, then run
 * `SLACK_BOT_TOKEN=xoxb-... npx tsx test/record-blocks-validate-cache.ts` to validate it for
 * real and write it into the cache.
 */
const cachePath = fileURLToPath(new URL("./fixtures/blocksValidateCache.json", import.meta.url));
const cacheExists = existsSync(cachePath);

// vitest reports a skipped suite by name, so an absent cache is still visible in test output as
// "blocksValidateCache.json not found - ..." rather than the file silently contributing nothing.
describe.skipIf(!cacheExists)(
  cacheExists
    ? "PermissionApprovalGateway blocks - cross-checked against cached Slack blocks.validate results"
    : "blocksValidateCache.json not found - run SLACK_BOT_TOKEN=xoxb-... npx tsx " +
        "test/record-blocks-validate-cache.ts once to generate it",
  () => {
    // describe.skipIf skips execution of the `it`s below but still runs this callback body to
    // discover them, so guard the read itself rather than relying on skipIf alone.
    const cache = cacheExists
      ? (JSON.parse(readFileSync(cachePath, "utf8")) as { cases: CachedCase[] })
      : { cases: [] };
    const cacheById = new Map(cache.cases.map((c) => [c.id, c]));

    for (const caseDef of BLOCKS_VALIDATE_CASES) {
      it(`${caseDef.id}: ${caseDef.description}`, async () => {
        const cached = cacheById.get(caseDef.id);
        if (cached === undefined) {
          throw new Error(
            `no cached result for case "${caseDef.id}" - run ` +
              "SLACK_BOT_TOKEN=xoxb-... npx tsx test/record-blocks-validate-cache.ts to add it",
          );
        }

        const messagingClient = fakeClient();
        const gateway = new PermissionApprovalGateway(messagingClient);
        const responder = gateway.createResponder(fakeContext(caseDef.context));
        void responder(fakeAsk(caseDef.ask), new AbortController().signal);
        await flush();
        const post = messagingClient.posted[0];
        if (post === undefined) throw new Error("expected a message to have been posted");

        expect(
          post.blocks,
          `blocks for "${caseDef.id}" changed since the cache was recorded - re-run ` +
            "test/record-blocks-validate-cache.ts with a live SLACK_BOT_TOKEN to re-validate " +
            "against Slack and refresh the cache before trusting the new shape",
        ).toEqual(cached.blocks);
        expect(cached.result.ok, JSON.stringify(cached.result.errors)).toBe(true);
      });
    }
  },
);
