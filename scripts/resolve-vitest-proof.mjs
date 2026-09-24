#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { isAbsolute, normalize, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";

const prefix = "vitest:";
const token = process.argv[2] ?? "";

function reject(message) {
  console.error(`vitest proof rejected: ${message}`);
  process.exit(1);
}

if (!token.startsWith(prefix)) {
  reject(`expected a ${prefix} token`);
}

const proof = token.slice(prefix.length);
const separator = proof.lastIndexOf("#");
if (separator <= 0 || separator === proof.length - 1) {
  reject("expected vitest:<repo-relative-path>#<base64url-title>");
}

// Each Node workspace that may hold a `vitest:` proof owns its own TypeScript
// compiler: the SDK's under sdk/typescript/node_modules, Studio's hoisted to
// apps/node_modules (typescript is a devDependency of the apps/ root
// package.json). The proof path selects which one parses it.
const workspaces = [
  {
    prefix: `sdk${sep}typescript${sep}`,
    compiler: "sdk/typescript/node_modules/typescript/lib/typescript.js",
  },
  {
    prefix: `apps${sep}`,
    compiler: "apps/node_modules/typescript/lib/typescript.js",
  },
];

const relativePath = normalize(proof.slice(0, separator));
const workspace =
  isAbsolute(relativePath) || relativePath === ".." || relativePath.startsWith(`..${sep}`)
    ? undefined
    : workspaces.find((candidate) => relativePath.startsWith(candidate.prefix));
if (workspace === undefined) {
  reject("test path must stay under sdk/typescript/ or apps/");
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
  const compilerPath = resolve(workspace.compiler);
  ts = (await import(pathToFileURL(compilerPath).href)).default;
} catch (error) {
  reject(
    `TypeScript is unavailable; run task sdk:install or task studio:install first (${error instanceof Error ? error.message : String(error)})`,
  );
}

const testPath = resolve(relativePath);
let sourceText;
try {
  sourceText = readFileSync(testPath, "utf8");
} catch (error) {
  reject(`cannot read ${relativePath}: ${error instanceof Error ? error.message : String(error)}`);
}

// React test files under apps/web are .tsx; parsing them as plain TS would
// misread JSX as type assertions and miss (or miscount) the titles.
const scriptKind = relativePath.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
const source = ts.createSourceFile(
  testPath,
  sourceText,
  ts.ScriptTarget.Latest,
  true,
  scriptKind,
);
let matches = 0;

function isTestCallee(expression) {
  if (
    ts.isCallExpression(expression) &&
    ts.isPropertyAccessExpression(expression.expression)
  ) {
    return isTestCallee(expression.expression.expression);
  }
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

console.error(`vitest proof resolved: ${relativePath} :: ${JSON.stringify(expectedTitle)}`);
