import { type Client, type EventOf, MecatlError } from "@stacklok-oss/mecatl-sdk";
import { connect, type DaemonInfo, query, spawn } from "@stacklok-oss/mecatl-sdk/deno";
import { HarnessService } from "@stacklok-oss/mecatl-sdk/gen";

const [binaryArgument, scratchArgument] = Deno.args;
if (binaryArgument === undefined || scratchArgument === undefined) {
  throw new Error("Deno integration requires the mecated binary and scratch directory arguments");
}
const binary = await Deno.realPath(binaryArgument);
const scratchPath = await Deno.realPath(scratchArgument);
const fixtureRoot = await Deno.makeTempDir({ dir: scratchPath, prefix: "sdk-deno-e2e-" });
const workspace = `${fixtureRoot}/workspace`;
await Deno.mkdir(workspace);

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

const daemonArgs = [
  "--mock",
  "--workspace",
  workspace,
  "--metrics-addr",
  "",
  "--no-soul",
  "--no-user-model",
  "--no-scheduler",
  "--flight-recorder=false",
] as const;
const daemonEnv = {
  XDG_CACHE_HOME: `${fixtureRoot}/cache`,
  XDG_CONFIG_HOME: `${fixtureRoot}/config`,
  XDG_DATA_HOME: `${fixtureRoot}/data`,
  XDG_STATE_HOME: `${fixtureRoot}/state`,
};

async function runtimeDirectories(): Promise<string[]> {
  const names: string[] = [];
  for await (const entry of Deno.readDir(fixtureRoot)) {
    if (entry.isDirectory && entry.name.startsWith("mecatl-sdk-deno-")) names.push(entry.name);
  }
  return names.sort();
}

async function expectSpawnFailure(options: Parameters<typeof spawn>[0]): Promise<void> {
  try {
    await spawn(options);
  } catch (error) {
    assert(error instanceof MecatlError, "Deno spawn failure was not a typed SDK error");
    assert(error.code === "spawn_failed", `unexpected Deno spawn error code ${error.code}`);
    return;
  }
  throw new Error("Deno spawn unexpectedly succeeded");
}

let failure: unknown;
try {
  assert(HarnessService.method.converse.methodKind === "bidi_streaming", "generated service drift");
  assert((await runtimeDirectories()).length === 0, "Deno runtime directory leaked before spawn");

  await expectSpawnFailure({ args: ["--http-addr"] });
  assert(
    (await runtimeDirectories()).length === 0,
    "rejected Deno argv created a runtime directory",
  );

  await expectSpawnFailure({
    args: [...daemonArgs, "--not-a-real-mecated-flag"],
    binaryPath: binary,
    env: daemonEnv,
    tempDirectory: fixtureRoot,
  });
  assert(
    (await runtimeDirectories()).length === 0,
    "failed Deno startup leaked its runtime directory",
  );

  const client: Client & { readonly daemon: DaemonInfo } = await spawn({
    args: daemonArgs,
    binaryPath: binary,
    env: daemonEnv,
    tempDirectory: fixtureRoot,
  });
  const daemonAddress = client.daemon.grpcAddress;
  try {
    assert(client.daemon.transport === "grpc", "Deno spawn did not select gRPC");
    assert(!("tool" in client), "Deno spawn exposed callback-tool authority");
    assert(!client.daemon.features.includes("mcp_servers_on_create"), "TCP granted MCP authority");
    assert(client.daemon.pid > 0, "Deno spawn did not publish a child pid");
    assert(
      client.daemon.grpcAddress.startsWith("127.0.0.1:"),
      "Deno spawn did not use loopback gRPC",
    );
    const directories = await runtimeDirectories();
    assert(directories.length === 1, "Deno spawn did not own one runtime directory");
    const ready = JSON.parse(
      await Deno.readTextFile(`${fixtureRoot}/${directories[0]}/ready.json`),
    ) as Record<string, unknown>;
    assert(ready.pid === client.daemon.pid, "Deno ready document belongs to a different child");
    assert(ready.grpc_address === daemonAddress, "Deno ready document changed the gRPC address");
    assert(!("http_address" in ready), "Deno spawn unexpectedly enabled an HTTP listener");

    const session = await client.sessions.create({});
    const run = await session.run("complete the Deno.Command integration");
    const resultPromise = run.result();
    const attachment = await session.attach(run.id);
    let attachedTerminal: EventOf<"result"> | undefined;
    for await (const envelope of attachment) {
      if (envelope.kind === "event" && envelope.event.kind === "result") {
        attachedTerminal = envelope.event;
      }
    }
    const result = await resultPromise;
    assert(result.stopReason === "end_turn", `unexpected stop reason ${result.stopReason}`);
    assert(result.text !== "", "Deno run returned an empty result");
    assert(attachedTerminal?.runId === result.runId, "Deno attachment reached a different run");
    assert(client.status.getSnapshot() === "online", "Deno client did not become online");
    await session.delete();
  } finally {
    await client.close();
  }
  assert(
    (await runtimeDirectories()).length === 0,
    "Deno client close leaked its runtime directory",
  );
  const disconnected = connect({ baseUrl: `http://${daemonAddress}` });
  try {
    await disconnected.sessions.create({});
    throw new Error("Deno client close left its daemon reachable");
  } catch (error) {
    if (error instanceof Error && error.message === "Deno client close left its daemon reachable") {
      throw error;
    }
  } finally {
    await disconnected.close();
  }

  const oneShot = await query("complete the Deno one-shot query", {
    spawn: {
      args: daemonArgs,
      binaryPath: binary,
      env: daemonEnv,
      tempDirectory: fixtureRoot,
    },
  });
  let queryTerminal: EventOf<"result"> | undefined;
  for await (const event of oneShot) {
    if (event.kind === "result") queryTerminal = event;
  }
  assert(queryTerminal?.payload.stop === "end_turn", "Deno query did not reach end_turn");
  assert((await runtimeDirectories()).length === 0, "Deno query leaked its daemon runtime");
} catch (error) {
  failure = error;
} finally {
  await Deno.remove(fixtureRoot, { recursive: true });
}

if (failure !== undefined) throw failure;
console.log(
  `Deno ${Deno.version.deno}: native spawn, gRPC stream/attach, query and cleanup passed`,
);
