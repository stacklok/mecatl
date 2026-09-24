#!/usr/bin/env node

import { execFileSync } from "node:child_process";

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

let listed;
try {
  listed = JSON.parse(execFileSync("task", ["--list", "--json"], { encoding: "utf8" }));
} catch (error) {
  reject(`could not list Task targets: ${error instanceof Error ? error.message : String(error)}`);
}
if (!Array.isArray(listed.tasks) || !listed.tasks.some((task) => task.name === proof)) {
  reject(`Task target ${JSON.stringify(proof)} is not registered`);
}
