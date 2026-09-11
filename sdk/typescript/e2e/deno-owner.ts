import { spawn } from "@stacklok-oss/mecatl-sdk/deno";

const [binaryPath, directory] = Deno.args;
if (binaryPath === undefined || directory === undefined) {
  throw new Error("Deno owner fixture requires a binary and runtime directory");
}
const client = await spawn({
  binaryPath,
  tempDirectory: directory,
  args: [
    "--mock",
    "--workspace",
    directory,
    "--metrics-addr",
    "",
    "--no-soul",
    "--no-user-model",
    "--no-scheduler",
    "--flight-recorder=false",
  ],
  env: {
    XDG_CACHE_HOME: `${directory}/cache`,
    XDG_CONFIG_HOME: `${directory}/config`,
    XDG_DATA_HOME: `${directory}/data`,
    XDG_STATE_HOME: `${directory}/state`,
  },
});
console.log(JSON.stringify(client.daemon));
// Exit without client.close(): only the OS closing stdin can stop the daemon.
Deno.exit(0);
