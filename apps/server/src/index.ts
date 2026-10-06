// SPDX-License-Identifier: Apache-2.0

import { fileURLToPath } from "node:url";
import { serve } from "@hono/node-server";
import { bootstrap, describeStartupFailure } from "./bootstrap.js";

const defaultWebDist = fileURLToPath(new URL("../../web/dist", import.meta.url));
const environment: NodeJS.ProcessEnv = {
  STUDIO_WEB_DIST: defaultWebDist,
  ...process.env,
};

let booted: Awaited<ReturnType<typeof bootstrap>>;
try {
  booted = await bootstrap({ environment });
} catch (error) {
  process.stderr.write(`mecatl-studio: ${describeStartupFailure(error)}\n`);
  process.exit(1);
}

const { app, config, logger, runtime } = booted;
const server = serve({ fetch: app.fetch, hostname: config.host, port: config.port }, (info) => {
  logger.info("studio.listening", {
    address: info.address,
    authMode: runtime.authMode,
    port: info.port,
    publicUrl: config.publicUrl?.toString(),
  });
});

let stopping = false;
const stop = () => {
  if (stopping) return;
  stopping = true;
  server.close(async (error) => {
    await runtime.close();
    if (error !== undefined) {
      logger.error("studio.shutdown_failed", { message: error.message });
      process.exitCode = 1;
    }
  });
};

process.once("SIGINT", stop);
process.once("SIGTERM", stop);
