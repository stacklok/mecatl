#!/usr/bin/env -S npx tsx
/**
 * Refreshes test/fixtures/blocksValidateCache.json: regenerates every case in
 * fixtures/blocksValidateCases.ts through the REAL PermissionApprovalGateway code, then
 * validates the result against Slack's real `blocks.validate` API - the same authority that
 * rejected mecatl#1986 in production, and the ground truth the offline test
 * (approvals.blocksValidate.test.ts) trusts instead of a hand-written invariant.
 *
 * Run this whenever:
 *  - you add, remove, or change a case in fixtures/blocksValidateCases.ts
 *  - approvals.blocksValidate.test.ts fails saying a case's blocks changed since the cache was
 *    recorded (meaning you changed how approvals.ts renders that case's shape)
 *
 * Requires a live Slack bot token: `blocks.validate` has no side effect (it never posts to a
 * channel), so any workspace's token works.
 *
 *   SLACK_BOT_TOKEN=xoxb-... npx tsx test/record-blocks-validate-cache.ts
 *
 * Never pass the token to anyone or anything other than this command in your own terminal - see
 * this repo's standing rule against handling live secret values on the agent's behalf.
 */
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { Block, KnownBlock } from "@slack/types";
import { WebClient } from "@slack/web-api";

import { PermissionApprovalGateway } from "../src/approvals.js";
import { BLOCKS_VALIDATE_CASES } from "./fixtures/blocksValidateCases.js";
import { fakeAsk, fakeClient, fakeContext, flush } from "./helpers.js";

const token = process.env.SLACK_BOT_TOKEN;
if (token === undefined) {
  console.error("SLACK_BOT_TOKEN is required - see this file's header comment for the command.");
  process.exit(1);
}
const client = new WebClient(token);

async function blocksFor(caseDef: (typeof BLOCKS_VALIDATE_CASES)[number]): Promise<unknown[]> {
  const messagingClient = fakeClient();
  const gateway = new PermissionApprovalGateway(messagingClient);
  const responder = gateway.createResponder(fakeContext(caseDef.context));
  void responder(fakeAsk(caseDef.ask), new AbortController().signal);
  await flush();
  const post = messagingClient.posted[0];
  if (post === undefined) {
    throw new Error(`case "${caseDef.id}": expected a message to have been posted`);
  }
  return post.blocks;
}

interface RecordedCase {
  id: string;
  description: string;
  ask: (typeof BLOCKS_VALIDATE_CASES)[number]["ask"];
  context: (typeof BLOCKS_VALIDATE_CASES)[number]["context"];
  blocks: unknown;
  result: { ok: boolean; errors?: unknown };
}

const cases: RecordedCase[] = [];
for (const caseDef of BLOCKS_VALIDATE_CASES) {
  const blocks = await blocksFor(caseDef);
  const response = await client.blocks.validate({
    message: { blocks: blocks as (Block | KnownBlock)[] },
  });
  const ok = response.ok ?? false;
  cases.push({
    ask: caseDef.ask,
    blocks,
    context: caseDef.context,
    description: caseDef.description,
    id: caseDef.id,
    result: { errors: response.errors, ok },
  });
  console.log(`${ok ? "OK  " : "FAIL"} ${caseDef.id}`);
}

const outPath = fileURLToPath(new URL("./fixtures/blocksValidateCache.json", import.meta.url));
writeFileSync(
  outPath,
  `${JSON.stringify({ cases, generatedAt: new Date().toISOString() }, null, 2)}\n`,
);
console.log(`\nWrote ${cases.length} cases to ${outPath}`);

const failed = cases.filter((c) => !c.result.ok);
if (failed.length > 0) {
  console.error(`\n${failed.length} case(s) failed Slack's validation:`);
  for (const c of failed) console.error(`  ${c.id}: ${JSON.stringify(c.result.errors)}`);
  process.exit(1);
}
