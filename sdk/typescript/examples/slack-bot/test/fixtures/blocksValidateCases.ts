import fc from "fast-check";

import type { fakeAsk, fakeContext } from "../helpers.js";

export interface BlocksValidateCase {
  /** Stable id - used as both the cache key and the test title, so renaming one after the cache
   * has been recorded orphans its cached entry (the offline test will say so clearly). */
  id: string;
  description: string;
  ask: Parameters<typeof fakeAsk>[0];
  context: Parameters<typeof fakeContext>[0];
}

/**
 * Hand-picked cases: one per distinct rendering path in `approvals.ts`, named for what they
 * exercise rather than left to chance. Add a case here (a) whenever a bug like mecatl#1986 turns
 * up a shape nobody tested, or (b) whenever `approvals.ts` grows a new rendering branch.
 */
const HAND_PICKED: BlocksValidateCase[] = [
  {
    ask: { args: JSON.stringify({ body: "" }) },
    context: {},
    description: "empty string arg value (mecatl#1986)",
    id: "empty-string-value",
  },
  {
    ask: { args: JSON.stringify({ "": "value" }) },
    context: {},
    description: "empty string as an arg key",
    id: "empty-string-key",
  },
  {
    ask: { args: "{}" },
    context: {},
    description: "no args at all - no table block should render, just the card",
    id: "empty-args-object",
  },
  {
    ask: { args: "not valid json" },
    context: {},
    description: "args that aren't valid JSON - single-cell raw-dump fallback path",
    id: "invalid-json-args",
  },
  {
    ask: {
      args: JSON.stringify(
        Object.fromEntries(Array.from({ length: 12 }, (_, i) => [`key${i}`, `value${i}`])),
      ),
    },
    context: {},
    description: "more than 10 keys - row cap + '...and N more' row path",
    id: "many-keys",
  },
  {
    ask: { args: JSON.stringify({ options: { recursive: true }, tags: ["a", "b"] }) },
    context: {},
    description: "object/array arg values - inline compact-JSON rendering path",
    id: "nested-value",
  },
  {
    ask: { args: JSON.stringify({ timeout: 30 }) },
    context: {},
    description: "numeric arg value - raw_number cell, not rich_text",
    id: "number-value",
  },
  {
    ask: { args: JSON.stringify({ body: "x".repeat(301) }) },
    context: {},
    description: "value one character past MAX_ARGS_CELL_VALUE (300) - clamp boundary",
    id: "long-value-clamp-boundary",
  },
  {
    ask: { args: JSON.stringify({ body: "🧪".repeat(160) }) },
    context: {},
    description:
      "multi-byte/emoji value long enough to clamp - checks clamp's slice() doesn't cut a " +
      "surrogate pair in half and produce invalid UTF-16",
    id: "unicode-value-clamped",
  },
  {
    ask: { args: "{}", reason: "" },
    context: { originLabel: "x".repeat(200) },
    description: "originLabel long enough to push the subtitle past the 150-char plain_text cap",
    id: "long-subtitle-boundary",
  },
  {
    ask: { args: "{}", tool: "T".repeat(200) },
    context: {},
    description: "tool name long enough to push the title past the 150-char plain_text cap",
    id: "long-title-boundary",
  },
];

/**
 * Deterministic fast-check sample (fixed seed) - broadens coverage past the hand-picked cases
 * above without making the cache non-reproducible: the same seed always regenerates the exact
 * same inputs, so re-running the recorder without changing SEED/SAMPLE_COUNT reproduces the
 * same case set byte-for-byte.
 *
 * To explore for new bugs (not just re-validate known cases), bump SAMPLE_COUNT or change SEED
 * locally, run the recorder against a live token, and promote anything interesting it finds to
 * a named entry in HAND_PICKED above before committing - a bare `fc-seed-*` id is meaningless in
 * a failure message a year from now.
 */
const SEED = 20260928;
const SAMPLE_COUNT = 25;

function fuzzedCases(): BlocksValidateCase[] {
  const argsArb = fc.dictionary(fc.string({ maxLength: 20 }), fc.jsonValue(), { maxKeys: 15 });
  const samples = fc.sample(argsArb, { numRuns: SAMPLE_COUNT, seed: SEED });
  return samples.map((args, i) => ({
    ask: { args: JSON.stringify(args) },
    context: {},
    description: `fast-check sample #${i} (seed ${SEED})`,
    id: `fc-seed-${SEED}-run-${i}`,
  }));
}

export const BLOCKS_VALIDATE_CASES: BlocksValidateCase[] = [...HAND_PICKED, ...fuzzedCases()];
