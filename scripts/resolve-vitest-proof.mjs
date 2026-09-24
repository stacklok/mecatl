#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { isAbsolute, normalize, sep } from "node:path";

const prefix = "vitest:";
const token = process.argv[2] ?? "";

function reject(message) {
  console.error(`vitest proof rejected: ${message}`);
  process.exit(1);
}

if (!token.startsWith(prefix)) reject(`expected a ${prefix} token`);
const proof = token.slice(prefix.length);
const separator = proof.lastIndexOf("#");
if (separator <= 0 || separator === proof.length - 1) reject("expected vitest:<repo-relative-path>#<base64url-title>");

const relativePath = normalize(proof.slice(0, separator));
const workspace = isAbsolute(relativePath) || relativePath === ".." || relativePath.startsWith(`..${sep}`)
  ? undefined
  : [
      { prefix: `sdk${sep}typescript${sep}` },
      { prefix: `apps${sep}` },
    ].find((candidate) => relativePath.startsWith(candidate.prefix));
if (!workspace) reject("test path must stay under sdk/typescript/ or apps/");

const encodedTitle = proof.slice(separator + 1);
if (!/^[A-Za-z0-9_-]+$/.test(encodedTitle)) reject("title must use unpadded base64url encoding");
const expectedTitle = Buffer.from(encodedTitle, "base64url").toString("utf8");
if (!expectedTitle) reject("test title must not be empty");

const indexPath = process.env.ACTRACE_VITEST_INDEX;
if (!indexPath) reject("ACTRACE_VITEST_INDEX is required; run task ac-trace:vitest-index first");
let index;
try {
  index = JSON.parse(readFileSync(indexPath, "utf8"));
} catch (error) {
  reject(`cannot read Vitest proof index: ${error instanceof Error ? error.message : String(error)}`);
}
const entry = index?.files?.[relativePath];
if (!entry || entry.workspace !== workspace.prefix) reject(`test source is not indexed: ${relativePath}`);
const matches = entry.titles?.[expectedTitle] ?? 0;
if (matches !== 1) reject(`expected exactly one test titled ${JSON.stringify(expectedTitle)} in ${relativePath}; found ${matches}`);
console.error(`vitest proof resolved: ${relativePath} :: ${JSON.stringify(expectedTitle)}`);
