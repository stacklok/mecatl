#!/usr/bin/env node

import { spawnSync } from "node:child_process";

const rawProof = process.argv[2] ?? "";
const proof = rawProof.endsWith("`") ? rawProof.slice(0, -1) : rawProof;
const allowed = new Set(["api:check", "test:engine-standalone", "site:build"]);

function reject(message) {
  console.error(`task proof rejected: ${message}`);
  process.exit(1);
}

if (!allowed.has(proof)) {
  reject(`unsupported task target ${JSON.stringify(rawProof)}`);
}

const result = spawnSync("task", [proof], { stdio: "inherit" });
if (result.error) {
  reject(`${proof} could not start: ${result.error.message}`);
}
if (result.status !== 0) {
  reject(`${proof} exited with status ${result.status ?? "unknown"}`);
}
