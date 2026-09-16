import { expect, it } from "vitest";

import type { DiagnosticRecord, Event, EventOf } from "../src/index.js";
import { query } from "../src/node.js";
import { collectRun } from "./harness.js";
import { childExit, spawnProductFixture, startRuntimeHelper } from "./product-harness.js";

const objectSchema = {
  additionalProperties: false,
  properties: { query: { type: "string" } },
  required: ["query"],
  type: "object",
} as const;

function resultFor(events: readonly Event[], callId: string): EventOf<"tool.result"> {
  const result = events.find(
    (event): event is EventOf<"tool.result"> =>
      event.kind === "tool.result" && event.payload.callId === callId,
  );
  if (result === undefined) throw new Error(`missing tool.result for ${callId}`);
  return result;
}

it("a scripted turn calls an sdk tool and receives its result", async () => {
  const instance = await spawnProductFixture("tool-lookup.json");
  try {
    let argument: unknown;
    instance.client.tool(
      "lookup",
      objectSchema,
      ({ query: value }) => {
        argument = value;
        return `lookup result: ${value}`;
      },
      { readOnly: true },
    );
    const session = await instance.client.sessions.create({});
    const { events, terminal } = await collectRun(
      await session.run("look it up", { onPermissionAsk: () => "allow_once" }),
    );

    expect(argument).toBe("aztec calendar");
    expect(resultFor(events, "lookup-1")).toMatchObject({
      payload: { content: "lookup result: aztec calendar", isError: false },
    });
    expect(terminal.payload).toMatchObject({
      stop: "end_turn",
      text: "the lookup result reached the model",
    });
  } finally {
    await instance.close();
  }
});

it("the tool round trip passes on bun", async () => {
  const helper = await startRuntimeHelper("bun", "roundtrip");
  try {
    await childExit(helper.child);
    expect(helper.child.exitCode, helper.stderr()).toBe(0);
    expect(helper.record).toMatchObject({
      content: "bun lookup: aztec calendar",
      invoked: true,
      runtimeRemoved: true,
      stop: "end_turn",
    });
  } finally {
    await helper.cleanup();
  }
});

it("a throwing handler is generic on the wire and the run continues", async () => {
  const diagnostics: DiagnosticRecord[] = [];
  const instance = await spawnProductFixture("tool-throw.json", {
    diagnostics: (record) => diagnostics.push(record),
  });
  try {
    instance.client.tool(
      "explode",
      {
        additionalProperties: false,
        properties: { input: { type: "string" } },
        required: ["input"],
        type: "object",
      },
      () => {
        throw new Error("private callback detail");
      },
    );
    const session = await instance.client.sessions.create({});
    const { events, terminal } = await collectRun(
      await session.run("call the throwing tool", { onPermissionAsk: () => "allow_once" }),
    );
    const result = resultFor(events, "throw-1");

    expect(result.payload.isError).toBe(true);
    expect(result.payload.content).toMatch(/failed \(correlation id: [0-9a-f]{24}\)/);
    expect(result.payload.content).not.toContain("private callback detail");
    expect(diagnostics).toHaveLength(1);
    expect(diagnostics[0]).toMatchObject({
      cause: expect.objectContaining({ message: "private callback detail" }),
      code: "tool_handler_failed",
      fields: { tool: "explode" },
    });
    expect(terminal.payload).toMatchObject({
      stop: "end_turn",
      text: "the run continued after the callback failure",
    });
  } finally {
    await instance.close();
  }
});

// Both halves run the SAME (default) posture so the only variable is the
// readOnly assertion. readOnlyHint drives the harness's read-parallel dispatch,
// NOT the permission layer, so a read-only callback still raises an ask; the
// observable difference is that two read-only calls overlap in flight while a
// mutating call is dispatched alone.
it("read-only and mutating tools both reach the model", async () => {
  const readOnly = await spawnProductFixture("tool-read-only.json");
  try {
    const asks: string[] = [];
    let inFlight = 0;
    let overlapped = false;
    let release: (() => void) | undefined;
    const bothArrived = new Promise<void>((resolve) => {
      release = resolve;
    });
    readOnly.client.tool(
      "inspect",
      {
        additionalProperties: false,
        properties: { key: { type: "string" } },
        required: ["key"],
        type: "object",
      },
      async ({ key }) => {
        inFlight += 1;
        if (inFlight > 1) {
          overlapped = true;
          release?.();
        }
        // Resolves as soon as the sibling call arrives; the timeout keeps a
        // serial dispatch from hanging the suite so the assertion, not a
        // deadlock, reports the regression.
        await Promise.race([bothArrived, new Promise((resolve) => setTimeout(resolve, 5_000))]);
        inFlight -= 1;
        return `inspected ${key}`;
      },
      { readOnly: true },
    );
    const session = await readOnly.client.sessions.create({});
    const { events } = await collectRun(
      await session.run("inspect", {
        onPermissionAsk: (ask) => {
          asks.push(ask.tool);
          return "allow_once";
        },
      }),
    );

    expect(overlapped).toBe(true);
    expect(resultFor(events, "read-1").payload.content).toBe("inspected status");
    expect(resultFor(events, "read-2").payload.content).toBe("inspected health");
    // readOnly is a dispatch hint, not a permission exemption.
    expect(asks).toEqual(["mcp__sdk__inspect", "mcp__sdk__inspect"]);
  } finally {
    await readOnly.close();
  }

  const mutating = await spawnProductFixture("tool-mutating.json");
  try {
    let invoked = false;
    mutating.client.tool(
      "store",
      {
        additionalProperties: false,
        properties: { value: { type: "string" } },
        required: ["value"],
        type: "object",
      },
      ({ value }) => {
        invoked = true;
        return `stored ${value}`;
      },
    );
    const session = await mutating.client.sessions.create({});
    const { events } = await collectRun(
      await session.run("store", { onPermissionAsk: () => "allow_once" }),
    );
    expect(invoked).toBe(true);
    expect(events).toContainEqual(
      expect.objectContaining({
        kind: "permission.ask",
        payload: expect.objectContaining({ tool: "mcp__sdk__store" }),
      }),
    );
    expect(resultFor(events, "mutate-1").payload.content).toBe("stored changed");
  } finally {
    await mutating.close();
  }
});

it("a mutating tool asks and a responder-less query denies it", async () => {
  const allowed = await spawnProductFixture("tool-mutating.json");
  try {
    let invoked = false;
    allowed.client.tool(
      "store",
      {
        additionalProperties: false,
        properties: { value: { type: "string" } },
        required: ["value"],
        type: "object",
      },
      () => {
        invoked = true;
        return "stored";
      },
    );
    const session = await allowed.client.sessions.create({});
    const { events } = await collectRun(
      await session.run("allow the mutation", { onPermissionAsk: () => "allow_once" }),
    );
    expect(events.some((event) => event.kind === "permission.ask")).toBe(true);
    expect(invoked).toBe(true);
  } finally {
    await allowed.close();
  }

  const diagnostics: DiagnosticRecord[] = [];
  const denied = await spawnProductFixture("tool-mutating.json", {
    diagnostics: (record) => diagnostics.push(record),
  });
  try {
    let invoked = false;
    denied.client.tool(
      "store",
      {
        additionalProperties: false,
        properties: { value: { type: "string" } },
        required: ["value"],
        type: "object",
      },
      () => {
        invoked = true;
        return "must not run";
      },
    );
    const oneShot = await query("deny the mutation", { client: denied.client });
    const events: Event[] = [];
    for await (const event of oneShot) events.push(event);

    expect(invoked).toBe(false);
    expect(events.some((event) => event.kind === "permission.ask")).toBe(true);
    expect(resultFor(events, "mutate-1").payload.isError).toBe(true);
    expect(events.at(-1)).toMatchObject({ kind: "result", payload: { stop: "end_turn" } });
    expect(diagnostics).toContainEqual(
      expect.objectContaining({
        code: "query_permission_ask_denied",
        fields: expect.objectContaining({ tool: "mcp__sdk__store" }),
      }),
    );
  } finally {
    await denied.close();
  }
});

it("the real go mcp client completes the handshake against the sdk host", async () => {
  const instance = await spawnProductFixture("tool-bad-arguments.json");
  try {
    let invoked = false;
    instance.client.tool("lookup", objectSchema, () => {
      invoked = true;
      return "must not run";
    });
    const session = await instance.client.sessions.create({});
    const { events, terminal } = await collectRun(
      await session.run("send invalid callback arguments", {
        onPermissionAsk: () => "allow_once",
      }),
    );
    const result = resultFor(events, "bad-args-1");

    expect(invoked).toBe(false);
    expect(result.payload).toMatchObject({ isError: true });
    expect(result.payload.content).toContain("do not match its schema");
    expect(terminal.payload.stop).toBe("end_turn");
  } finally {
    await instance.close();
  }
});

// Previously pinned a known limitation (recorded in the acceptance plan): the
// root capability set was minted once from the process-wide catalog, with no
// visibility into a session's own client-mounted MCP tools, so the default
// evaluator denied the call even after the permission ask had already been
// allowed. Fixed by threading SessionEngineResult.MountedClientMCPTools
// through setPerSessionLabels (internal/adapter/server), which folds a
// session's own mounted tool names into its minted authority -- this test now
// exercises that widening directly. Every other test in this file selects the
// no-op evaluator to exercise the SDK half in isolation; this one restores the
// default specifically to prove the widening authorizes the call, not just
// that the tool mounts and dispatches.
it("a callback tool is authorized under the default capability-set evaluator", async () => {
  const instance = await spawnProductFixture("tool-lookup.json", {
    args: ["--authority-evaluator", "local"],
  });
  try {
    let argument: unknown;
    instance.client.tool(
      "lookup",
      objectSchema,
      ({ query: value }) => {
        argument = value;
        return `lookup result: ${value}`;
      },
      { readOnly: true },
    );
    const session = await instance.client.sessions.create({});
    const { events, terminal } = await collectRun(
      await session.run("look it up", { onPermissionAsk: () => "allow_once" }),
    );

    expect(argument).toBe("aztec calendar");
    expect(resultFor(events, "lookup-1")).toMatchObject({
      payload: { content: "lookup result: aztec calendar", isError: false },
    });
    expect(terminal.payload).toMatchObject({
      stop: "end_turn",
      text: "the lookup result reached the model",
    });
  } finally {
    await instance.close();
  }
});
