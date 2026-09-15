import { spawn } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const controllerScript = resolve(root, "scripts/local-controller.mjs");
const next = resolve(root, "node_modules/.bin/next");
const production = process.argv.includes("--production");
const externalMode = Boolean(process.env.MECATL_BASE_URL?.trim());

let stopping = false;
let controller = null;
let web = null;
let controllerRestart = null;
let webRestart = null;

const delayRestart = (callback) => setTimeout(callback, 750);

async function controllerIsHealthy() {
  try {
    const response = await fetch("http://127.0.0.1:8788/status", {
      signal: AbortSignal.timeout(1_500),
    });
    return response.ok;
  } catch {
    return false;
  }
}

async function ensureController() {
  if (externalMode || stopping || controller || (await controllerIsHealthy()))
    return;
  controller = spawn(process.execPath, [controllerScript], {
    cwd: root,
    env: process.env,
    stdio: "inherit",
  });
  controller.once("exit", () => {
    controller = null;
    if (!stopping)
      controllerRestart = delayRestart(() => {
        controllerRestart = null;
        void ensureController();
      });
  });
}

function startWeb() {
  if (stopping || web) return;
  web = spawn(next, production ? ["start"] : ["dev"], {
    cwd: root,
    env: process.env,
    stdio: "inherit",
  });
  web.once("exit", () => {
    web = null;
    if (!stopping)
      webRestart = delayRestart(() => {
        webRestart = null;
        startWeb();
      });
  });
}

async function shutdown() {
  if (stopping) return;
  stopping = true;
  if (controllerRestart) clearTimeout(controllerRestart);
  if (webRestart) clearTimeout(webRestart);
  clearInterval(healthCheck);
  controller?.kill("SIGTERM");
  web?.kill("SIGTERM");
  setTimeout(() => process.exit(0), 2_000).unref();
}

for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, shutdown);

await ensureController();
startWeb();
const healthCheck = setInterval(() => {
  void ensureController();
}, 3_000);
