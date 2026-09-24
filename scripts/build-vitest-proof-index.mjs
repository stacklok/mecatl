#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { lstatSync, mkdirSync, realpathSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, isAbsolute, normalize, relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";

const output = process.env.ACTRACE_VITEST_INDEX;
if (!output) {
  throw new Error("ACTRACE_VITEST_INDEX is required");
}

const workspaces = [
  { prefix: `sdk${sep}typescript${sep}`, compiler: "sdk/typescript/node_modules/typescript/lib/typescript.js" },
  { prefix: `apps${sep}`, compiler: "apps/node_modules/typescript/lib/typescript.js" },
];

function trackedPaths() {
  const output = execFileSync("git", ["ls-files", "-z", "--", "sdk/typescript", "apps"], { encoding: "buffer" });
  return output.toString("utf8").split("\0").filter(Boolean);
}

function testWorkspace(path) {
  const normalized = normalize(path);
  return workspaces.find((workspace) => normalized.startsWith(workspace.prefix));
}

function isTestSource(path) {
  return /(?:\.test|\.e2e\.test)\.tsx?$/.test(path);
}

function isTestCallee(ts, expression) {
  if (ts.isCallExpression(expression) && ts.isPropertyAccessExpression(expression.expression)) {
    return isTestCallee(ts, expression.expression.expression);
  }
  if (ts.isIdentifier(expression)) return expression.text === "test" || expression.text === "it";
  if (ts.isPropertyAccessExpression(expression)) return isTestCallee(ts, expression.expression);
  return false;
}

const index = { version: 1, files: {} };
for (const path of trackedPaths()) {
  if (!isTestSource(path)) continue;
  const workspace = testWorkspace(path);
  if (!workspace) continue;
  const absolute = resolve(path);
  const rootPath = resolve(workspace.prefix);
  if (lstatSync(absolute).isSymbolicLink() || relative(rootPath, realpathSync(absolute)).startsWith(`..${sep}`)) {
    throw new Error(`test source escapes workspace: ${path}`);
  }
  const compiler = (await import(pathToFileURL(resolve(workspace.compiler)).href)).default;
  const source = compiler.createSourceFile(
    absolute,
    readFileSync(absolute, "utf8"),
    compiler.ScriptTarget.Latest,
    true,
    path.endsWith(".tsx") ? compiler.ScriptKind.TSX : compiler.ScriptKind.TS,
  );
  const titles = {};
  function visit(node) {
    if (compiler.isCallExpression(node) && isTestCallee(compiler, node.expression)) {
      const title = node.arguments[0];
      if (compiler.isStringLiteral(title) || compiler.isNoSubstitutionTemplateLiteral(title)) {
        titles[title.text] = (titles[title.text] ?? 0) + 1;
      }
    }
    compiler.forEachChild(node, visit);
  }
  visit(source);
  index.files[path] = { workspace: workspace.prefix, titles };
}
mkdirSync(dirname(output), { recursive: true });
writeFileSync(output, `${JSON.stringify(index)}\n`, { mode: 0o600 });
