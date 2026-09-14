import { Http2SessionManager } from "@connectrpc/connect-node";
import { type Client, MecatlError } from "@stacklok-oss/mecatl-sdk";
import { connect, type DenoConnectOptions } from "@stacklok-oss/mecatl-sdk/deno";

const invalidNodeOptions: DenoConnectOptions = {
  baseUrl: "http://127.0.0.1",
  nodeOptions: {
    // @ts-expect-error Native HTTP/2 options must reject unknown fields.
    notAnHttp2Option: true,
  },
};
void invalidNodeOptions;

const [binaryPath, fixtureRoot, mode] = Deno.args;
if (
  binaryPath === undefined ||
  fixtureRoot === undefined ||
  !["tcp", "tls", "uds"].includes(mode ?? "")
) {
  throw new Error("Deno gRPC fixture needs binary, directory, and tcp/tls/uds mode");
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

async function bounded<T>(operation: Promise<T>, label: string, milliseconds = 10_000): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      operation,
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(`Timed out: ${label}`)), milliseconds);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function drain(iterator: AsyncIterator<unknown>): Promise<void> {
  while (!(await iterator.next()).done) {
    // Buffered items are not proof that the stream has terminated.
  }
}

type NativeSession = NonNullable<Awaited<ReturnType<Http2SessionManager["request"]>>["session"]>;

async function verify(client: Client): Promise<void> {
  const sessions = new Map<NativeSession, Promise<void>>();
  const originalRequest = Http2SessionManager.prototype.request;
  // Observe the native sessions used by the real transport without replacing its requests.
  Http2SessionManager.prototype.request = async function (
    this: Http2SessionManager,
    ...args: Parameters<typeof originalRequest>
  ) {
    const stream = await originalRequest.apply(this, args);
    const session = stream.session;
    assert(session !== undefined, "ConnectRPC request did not expose its native HTTP/2 session");
    if (!sessions.has(session)) {
      sessions.set(session, new Promise<void>((resolve) => session.once("close", resolve)));
    }
    return stream;
  };
  try {
    assert(!("tool" in client), "Deno connect exposed callback-tool registration");
    const session = await bounded(client.sessions.create({}), "createSession");
    const run = await session.run("cancel this active scripted run");
    let sawActiveTurn = false;
    let cancelled = false;
    for await (const event of run) {
      if (event.kind === "turn.start") {
        sawActiveTurn = true;
        await run.cancel();
      }
      if (event.kind === "result") cancelled = event.payload.stop === "cancelled";
    }
    assert(sawActiveTurn, "cancel never observed the scripted run actively executing");
    assert(cancelled, "server cancellation did not return a cancelled terminal result");

    const running = await session.run("close while the gRPC stream is active");
    const iterator = running[Symbol.asyncIterator]();
    for (;;) {
      const item = await iterator.next();
      assert(!item.done, "scripted run completed before close could cancel its stream");
      if (item.value.kind === "turn.start") break;
    }
    const attachment = await session.attach(running.id);
    const watch = attachment[Symbol.asyncIterator]();
    assert(!(await watch.next()).done, "durable watch did not start before client close");
    assert(sessions.size > 0, "gRPC qualification did not observe a native HTTP/2 session");
    assert(
      [...sessions.keys()].some((native) => !native.closed && !native.destroyed),
      "native HTTP/2 session closed before client disposal",
    );
    await bounded(client.close(), "active gRPC client disposal");
    // Drain through buffered events until each consumer reaches done or rejects.
    await bounded(Promise.allSettled([drain(iterator), drain(watch)]), "stream/watch abort");
    // The daemon is still alive: its shutdown must not conceal a leaked client session.
    await bounded(Promise.all(sessions.values()), "native HTTP/2 session close");
    await client[Symbol.asyncDispose]();
    let rejected = false;
    try {
      await client.sessions.get(session.id);
    } catch (error) {
      rejected = error instanceof MecatlError && error.code === "invalid_state";
    }
    assert(rejected, "disposed Deno client still admitted RPCs");
  } finally {
    Http2SessionManager.prototype.request = originalRequest;
    await client.close();
  }
}

// The runner reserves a short private scratch directory for the UDS lane.
Deno.chdir(fixtureRoot);
const socketPath = `${fixtureRoot}/g.sock`;
const args = [
  "serve",
  "--mock",
  "--mock-script",
  "cancel.json",
  "--workspace",
  fixtureRoot,
  "--ready-file",
  `${fixtureRoot}/ready.json`,
  "--http-addr",
  "",
  "--metrics-addr",
  "",
  "--no-soul",
  "--no-user-model",
  "--no-scheduler",
  "--flight-recorder=false",
  "--lifetime-stdin",
  ...(mode === "uds" ? ["--grpc-unix-socket", socketPath] : ["--grpc-addr", "127.0.0.1:0"]),
  ...(mode === "tls" ? ["--tls-cert", "server.pem", "--tls-key", "server-key.pem"] : []),
];
const child = new Deno.Command(binaryPath, {
  args,
  cwd: fixtureRoot,
  clearEnv: true,
  env: {
    XDG_CACHE_HOME: `${fixtureRoot}/cache`,
    XDG_CONFIG_HOME: `${fixtureRoot}/config`,
    XDG_DATA_HOME: `${fixtureRoot}/data`,
    XDG_STATE_HOME: `${fixtureRoot}/state`,
  },
  stdin: "piped",
  stdout: "null",
  stderr: "piped",
}).spawn();
const lifetime = child.stdin.getWriter();
const stderr = new Response(child.stderr).text();
let exited = false;
const status = child.status.then((value) => {
  exited = true;
  return value;
});
let shutdownFailure: Error | undefined;
try {
  const ready = await bounded(
    (async () => {
      for (;;) {
        if (exited) throw new Error(`Remote daemon failed: ${await stderr}`);
        try {
          const document = JSON.parse(await Deno.readTextFile("ready.json"));
          assert(document.schema === "mecated-ready/1", "unexpected ready schema");
          assert(document.pid === child.pid, "remote readiness named a different process");
          assert(document.http_address === undefined, "gRPC qualification opened an HTTP listener");
          return document;
        } catch (error) {
          if (!(error instanceof Deno.errors.NotFound)) throw error;
        }
        await new Promise((resolve) => setTimeout(resolve, 20));
      }
    })(),
    "remote daemon readiness",
  );
  const ca = await Deno.readTextFile("ca.pem");
  const options: DenoConnectOptions =
    mode === "uds"
      ? { socketPath }
      : {
          baseUrl: `${mode === "tls" ? "https" : "http"}://${ready.grpc_address}`,
          ...(mode === "tls" ? { nodeOptions: { ca } } : {}),
        };

  if (mode === "tls") {
    const untrusted = connect({ baseUrl: `https://${ready.grpc_address}` });
    let rejected = false;
    try {
      await bounded(untrusted.sessions.create({}), "untrusted TLS", 3_000);
    } catch {
      rejected = true;
    } finally {
      await untrusted.close();
    }
    assert(rejected, "Deno TLS accepted an untrusted test CA");
  }
  await bounded(verify(connect(options)), `${mode} RPC and cancellation qualification`, 20_000);
  // Connected clients have transport ownership but must never stop this independently owned daemon.
  const observer = connect(options);
  try {
    const session = await bounded(observer.sessions.create({}), "remote ownership check");
    assert(session.id !== "", "closing connected client stopped the remote daemon");
    await session.delete();
  } finally {
    await observer.close();
  }
  console.log(
    `Deno ${Deno.version.deno}: ${mode} gRPC, active cancellation, stream/watch and HTTP/2 session disposal passed`,
  );
} finally {
  await lifetime.close().catch(() => {});
  lifetime.releaseLock();
  try {
    await bounded(status, "fixture daemon shutdown", 5_000);
  } catch {
    if (!exited) child.kill("SIGKILL");
    await bounded(status, "fixture daemon kill", 5_000);
    shutdownFailure = new Error(`Remote daemon failed stdin shutdown: ${await stderr}`);
  }
  await stderr;
}
if (shutdownFailure !== undefined) throw shutdownFailure;
