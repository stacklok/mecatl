import { type ChildProcessWithoutNullStreams, spawn, spawnSync } from "node:child_process";
import { createServer } from "node:http";
import { join } from "node:path";

import { expect, it } from "vitest";

import { fixture, repositoryRoot, withAuthorizationDaemon, withDaemon } from "./harness.js";

const sdkRoot = join(repositoryRoot, "sdk", "typescript");
const compiledExamples = join(sdkRoot, ".api-extractor-temp", "examples");

function compileExamples(): void {
  const result = spawnSync(
    "pnpm",
    [
      "exec",
      "tsc",
      "-p",
      "examples/tsconfig.json",
      "--noEmit",
      "false",
      "--outDir",
      compiledExamples,
    ],
    { cwd: sdkRoot, encoding: "utf8" },
  );
  if (result.status !== 0) {
    throw new Error(`example compilation failed:\n${result.stdout}\n${result.stderr}`);
  }
}

interface RunningExample {
  readonly child: ChildProcessWithoutNullStreams;
  readonly output: () => string;
  finish(): Promise<string>;
  waitFor(pattern: RegExp): Promise<RegExpMatchArray>;
}

function startExample(name: string, baseUrl: string): RunningExample {
  const child = spawn(process.execPath, [join(compiledExamples, `${name}.js`)], {
    cwd: sdkRoot,
    env: { ...process.env, MECATL_URL: baseUrl },
    stdio: ["pipe", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk: string) => {
    stdout += chunk;
  });
  child.stderr.on("data", (chunk: string) => {
    stderr += chunk;
  });
  const timeout = setTimeout(() => child.kill("SIGKILL"), 20_000);
  const exited = new Promise<number>((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => {
      clearTimeout(timeout);
      resolve(code ?? -1);
    });
  });
  return {
    child,
    output: () => stdout,
    finish: async () => {
      const code = await exited;
      if (code !== 0) {
        throw new Error(`${name} exited ${code}\nstdout:\n${stdout}\nstderr:\n${stderr}`);
      }
      return stdout;
    },
    waitFor: (pattern) =>
      new Promise((resolve, reject) => {
        const check = () => {
          const match = stdout.match(pattern);
          if (match !== null) {
            cleanup();
            resolve(match);
          } else if (child.exitCode !== null) {
            cleanup();
            reject(
              new Error(`${name} exited before ${pattern}\nstdout:\n${stdout}\nstderr:\n${stderr}`),
            );
          }
        };
        const cleanup = () => {
          child.stdout.off("data", check);
          child.off("exit", check);
          clearTimeout(waitTimeout);
        };
        const waitTimeout = setTimeout(() => {
          cleanup();
          reject(
            new Error(`${name} did not emit ${pattern}\nstdout:\n${stdout}\nstderr:\n${stderr}`),
          );
        }, 15_000);
        child.stdout.on("data", check);
        child.on("exit", check);
        check();
      }),
  };
}

async function withEnrollmentFixture<T>(
  run: (baseUrl: string, complete: () => void, calls: string[]) => Promise<T>,
): Promise<T> {
  const calls: string[] = [];
  let completed = false;
  const sessionId = "example-enrollment-session";
  const enrollmentId = "example-enrollment-id";
  const server = createServer((request, response) => {
    request.resume();
    const path = new URL(request.url ?? "/", "http://127.0.0.1").pathname;
    calls.push(`${request.method} ${path}`);
    let value: unknown;
    if (path === "/v1/compatibility") {
      value = {
        api_major: 1,
        capabilities: { mcp_connector_status: true, workspace_enrollment: true },
        features: ["workspace_enrollment"],
      };
    } else if (path === "/v1/sessions" && request.method === "POST") {
      value = { session_id: sessionId };
    } else if (path === `/v1/sessions/${sessionId}/mcp/connectors`) {
      value = {
        availability: "available",
        connectors: [{ catalogue_state: "declared", name: "calendar", tool_count: 0 }],
        enrollment_state: "not_started",
        total_connectors: 1,
      };
    } else if (path === `/v1/sessions/${sessionId}/workspace-enrollment/connect`) {
      value = completed
        ? { enrollment_id: enrollmentId, required_services: 1, status: "connected" }
        : {
            enrollment_id: enrollmentId,
            presentation_url: "https://example.com/authorize",
            required_services: 1,
            status: "pending",
          };
    } else {
      response.writeHead(404);
      response.end();
      return;
    }
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify(value));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("HTTP fixture has no port");
  try {
    return await run(
      `http://127.0.0.1:${address.port}`,
      () => {
        completed = true;
      },
      calls,
    );
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error === undefined ? resolve() : reject(error))),
    );
  }
}

it("all four workflow examples execute against offline fixtures", async () => {
  compileExamples();

  await withDaemon({ http: true }, async (daemon) => {
    const baseUrl = `http://${daemon.ready.http_address}`;
    const lifecycle = await startExample("session-lifecycle", baseUrl).finish();
    expect(lifecycle).toContain("Renamed title: SDK lifecycle example");
    expect(lifecycle).toContain("Forked session:");
    expect(lifecycle).toContain("Empty-history successor:");

    const discovery = await startExample("capability-discovery", baseUrl).finish();
    expect(discovery).toContain("API major: 1");
    expect(discovery).toContain("Scheduling available:");
    expect(discovery).toContain("Server implementation:");
  });

  await withEnrollmentFixture(async (baseUrl, complete, calls) => {
    const enrollment = startExample("mcp-workspace-enrollment", baseUrl);
    await enrollment.waitFor(/Complete enrollment at: https:\/\/example\.com\/authorize/u);
    complete();
    enrollment.child.stdin.end("\n");
    const output = await enrollment.finish();
    expect(output).toContain("Connectors: calendar");
    expect(output).toContain("Enrollment: pending");
    expect(output).toContain("Enrollment after recheck: connected");
    expect(calls.filter((call) => call.endsWith("/workspace-enrollment/connect"))).toHaveLength(2);
  });

  await withAuthorizationDaemon(
    { script: fixture("mcp-authorization-example.json") },
    async (daemon) => {
      const authorization = startExample(
        "mcp-authorization",
        `http://${daemon.ready.grpc_address}`,
      );
      const match = await authorization.waitFor(/Complete authorization at: (\S+)/u);
      const presentationUrl = match[1];
      if (presentationUrl === undefined) throw new Error("authorization example omitted its URL");
      await daemon.completeAuthorization(presentationUrl, "grant");
      authorization.child.stdin.end("\n");
      expect(await authorization.finish()).toContain("authorization example completed");
    },
  );
}, 120_000);
