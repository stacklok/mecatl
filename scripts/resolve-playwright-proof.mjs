#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { isAbsolute, normalize, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";

const prefix = "playwright:";
const token = process.argv[2] ?? "";

function reject(message) {
  console.error(`playwright proof rejected: ${message}`);
  process.exit(1);
}

if (!token.startsWith(prefix)) {
  reject(`expected a ${prefix} token`);
}

const proof = token.slice(prefix.length);
const separator = proof.lastIndexOf("#");
if (separator <= 0 || separator === proof.length - 1) {
  reject("expected playwright:<repo-relative-path>#<base64url-title>");
}

const relativePath = normalize(proof.slice(0, separator));
if (
  isAbsolute(relativePath) ||
  relativePath === ".." ||
  relativePath.startsWith(`..${sep}`) ||
  !relativePath.startsWith(`sdk${sep}typescript${sep}`)
) {
  reject("test path must stay under sdk/typescript/");
}

const encodedTitle = proof.slice(separator + 1);
if (!/^[A-Za-z0-9_-]+$/.test(encodedTitle)) {
  reject("title must use unpadded base64url encoding");
}
const expectedTitle = Buffer.from(encodedTitle, "base64url").toString("utf8");
if (expectedTitle.length === 0) {
  reject("test title must not be empty");
}

let ts;
try {
  const compilerPath = resolve("sdk/typescript/node_modules/typescript/lib/typescript.js");
  ts = (await import(pathToFileURL(compilerPath).href)).default;
} catch (error) {
  reject(
    `TypeScript is unavailable; run task sdk:install first (${error instanceof Error ? error.message : String(error)})`,
  );
}

const testPath = resolve(relativePath);
let sourceText;
try {
  sourceText = readFileSync(testPath, "utf8");
} catch (error) {
  reject(`cannot read ${relativePath}: ${error instanceof Error ? error.message : String(error)}`);
}

const source = ts.createSourceFile(
  testPath,
  sourceText,
  ts.ScriptTarget.Latest,
  true,
  ts.ScriptKind.TS,
);
let matches = 0;

function isTestCallee(expression) {
  if (ts.isIdentifier(expression)) {
    return expression.text === "test" || expression.text === "it";
  }
  if (ts.isPropertyAccessExpression(expression)) {
    return isTestCallee(expression.expression);
  }
  return false;
}

function visit(node) {
  if (ts.isCallExpression(node) && isTestCallee(node.expression)) {
    const title = node.arguments[0];
    if (
      (ts.isStringLiteral(title) || ts.isNoSubstitutionTemplateLiteral(title)) &&
      title.text === expectedTitle
    ) {
      matches += 1;
    }
  }
  ts.forEachChild(node, visit);
}

visit(source);
if (matches !== 1) {
  reject(
    `expected exactly one test titled ${JSON.stringify(expectedTitle)} in ${relativePath}; found ${matches}`,
  );
}

console.error(`playwright proof resolved: ${relativePath} :: ${JSON.stringify(expectedTitle)}`);
