import { App, LogLevel } from "@slack/bolt";
import { EmailAllowlistResolver } from "./access.js";
import { registerAgentSessions } from "./agentSessions.js";
import { MecatlBridge } from "./bridge.js";
import { loadConfig } from "./env.js";

async function main(): Promise<void> {
  const config = loadConfig();

  const app = new App({
    appToken: config.slackAppToken,
    logLevel: process.env.SLACK_LOG_LEVEL === "debug" ? LogLevel.DEBUG : LogLevel.INFO,
    socketMode: true,
    token: config.slackBotToken,
  });

  const bridge = new MecatlBridge(config.mecatlTarget);
  const resolver = new EmailAllowlistResolver(
    app.client,
    { allowedEmailDomains: config.allowedEmailDomains, allowedEmails: config.allowedEmails },
    app.logger,
  );
  registerAgentSessions(app, bridge, config, resolver);

  let shuttingDown = false;
  const shutdown = async (): Promise<void> => {
    if (shuttingDown) return;
    shuttingDown = true;
    await app.stop();
    await bridge.close();
    process.exit(0);
  };
  process.on("SIGINT", () => void shutdown());
  process.on("SIGTERM", () => void shutdown());

  await app.start();
  app.logger.info("mecatl Slack bot is running (Socket Mode)");
}

main().catch((error: unknown) => {
  console.error("mecatl Slack bot failed to start:", error);
  process.exitCode = 1;
});
