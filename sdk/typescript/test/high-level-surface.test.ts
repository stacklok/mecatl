import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRouterTransport } from "@connectrpc/connect";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { ScheduleService } from "../src/gen/mecatl/v1/schedule_pb.js";
import {
  type Client,
  connect,
  type McpAuthorization,
  type RunControls,
  type Session,
  type Team,
} from "../src/index.js";
import { RPC_CATALOG } from "../src/rpc-catalog.js";

type MethodName<T> = {
  [K in keyof T]-?: T[K] extends (...args: infer _Arguments) => unknown ? K & string : never;
}[keyof T];
type ClientNamespaceOperation = {
  [K in keyof Client]-?: NonNullable<Client[K]> extends object
    ? `Client.${K & string}.${MethodName<NonNullable<Client[K]>>}`
    : never;
}[keyof Client];
type PublicOperation =
  | ClientNamespaceOperation
  | `Session.${MethodName<Session>}`
  | `McpAuthorization.${MethodName<McpAuthorization>}`
  | `RunControls.${MethodName<RunControls>}`
  | `Team.${MethodName<Team>}`;
type Classification =
  | { readonly kind: "session" | "namespace" | "lifecycle"; readonly operation: PublicOperation }
  | { readonly kind: "raw-only"; readonly rationale: string };

// This test-only inventory describes the ergonomic SDK surface, not transport reachability.
// The operation is a published member; the source witness below must reach its exact RPC.
const HIGH_LEVEL_SURFACE = {
  "HarnessService.GetCompatibilityInfo": {
    kind: "namespace",
    operation: "Client.server.compatibility",
  },
  "HarnessService.CreateSession": { kind: "namespace", operation: "Client.sessions.create" },
  "HarnessService.GetServerInfo": { kind: "namespace", operation: "Client.server.info" },
  "HarnessService.GetSession": { kind: "session", operation: "Session.snapshot" },
  "HarnessService.ListGuardrailCoverage": {
    kind: "session",
    operation: "Session.guardrailCoverage",
  },
  "HarnessService.GetGuardrailReviewDetail": {
    kind: "session",
    operation: "Session.guardrailReviewDetail",
  },
  "HarnessService.GetSessionTranscript": { kind: "session", operation: "Session.transcript" },
  "HarnessService.SetMode": { kind: "session", operation: "Session.setMode" },
  "HarnessService.CloseSession": { kind: "session", operation: "Session.close" },
  "HarnessService.RenameSession": { kind: "session", operation: "Session.rename" },
  "HarnessService.DeleteSession": { kind: "session", operation: "Session.delete" },
  "HarnessService.CompactSession": { kind: "session", operation: "Session.compact" },
  "HarnessService.ClearSession": { kind: "session", operation: "Session.clear" },
  "HarnessService.ForkSession": { kind: "namespace", operation: "Client.sessions.fork" },
  "HarnessService.Converse": { kind: "session", operation: "Session.run" },
  "HarnessService.ResolveRunAsk": { kind: "lifecycle", operation: "RunControls.resolveAsk" },
  "HarnessService.CancelRun": { kind: "lifecycle", operation: "RunControls.cancel" },
  "HarnessService.SteerRun": { kind: "lifecycle", operation: "RunControls.steer" },
  "HarnessService.CancelRunSteer": { kind: "lifecycle", operation: "RunControls.cancelSteer" },
  "HarnessService.ListMcpResources": { kind: "namespace", operation: "Client.mcp.listResources" },
  "HarnessService.ReadMcpResource": { kind: "namespace", operation: "Client.mcp.readResource" },
  "HarnessService.ListMcpPrompts": { kind: "namespace", operation: "Client.mcp.listPrompts" },
  "HarnessService.GetMcpPrompt": { kind: "namespace", operation: "Client.mcp.getPrompt" },
  "HarnessService.ListMcpSources": { kind: "namespace", operation: "Client.mcp.listSources" },
  "HarnessService.RefreshMcpSources": { kind: "namespace", operation: "Client.mcp.refresh" },
  "HarnessService.ListToolHiveGroups": {
    kind: "namespace",
    operation: "Client.mcp.listToolHiveGroups",
  },
  "HarnessService.ListAgents": { kind: "namespace", operation: "Client.agents.list" },
  "HarnessService.ListCommands": { kind: "namespace", operation: "Client.commands.list" },
  "HarnessService.ListWorktrees": { kind: "namespace", operation: "Client.worktrees.list" },
  "HarnessService.StreamSessionEvents": {
    kind: "raw-only",
    rationale:
      "Whole-log readback has no cursor or follow; Session.activity() uses WatchSessionEvents for durable activity.",
  },
  "HarnessService.StreamSessionLive": {
    kind: "raw-only",
    rationale:
      "This process-local gRPC subscription is not the durable WatchSessionEvents path used by Session.attach().",
  },
  "HarnessService.WatchSessionEvents": { kind: "session", operation: "Session.activity" },
  "HarnessService.ListSessions": { kind: "namespace", operation: "Client.sessions.list" },
  "HarnessService.GetStorageHealth": { kind: "namespace", operation: "Client.storage.getHealth" },
  "HarnessService.PlanSessionCleanup": {
    kind: "namespace",
    operation: "Client.storage.planCleanup",
  },
  "HarnessService.ApplySessionCleanup": {
    kind: "namespace",
    operation: "Client.storage.applyCleanup",
  },
  "HarnessService.CancelSessionCleanup": {
    kind: "namespace",
    operation: "Client.storage.cancelCleanup",
  },
  "HarnessService.GetSessionCleanupJob": {
    kind: "namespace",
    operation: "Client.storage.getCleanupJob",
  },
  "HarnessService.ListSkills": { kind: "namespace", operation: "Client.skills.list" },
  "HarnessService.GetSoul": { kind: "namespace", operation: "Client.soul.get" },
  "HarnessService.GetUserModel": { kind: "namespace", operation: "Client.userModel.get" },
  "HarnessService.ReflectSession": { kind: "namespace", operation: "Client.reflection.reflect" },
  "HarnessService.GetLearningAttempt": {
    kind: "namespace",
    operation: "Client.learningAttempts.get",
  },
  "HarnessService.ListLearningAttempts": {
    kind: "namespace",
    operation: "Client.learningAttempts.list",
  },
  "HarnessService.RetryLearningAttempt": {
    kind: "namespace",
    operation: "Client.learningAttempts.retry",
  },
  "HarnessService.AbandonLearningAttempt": {
    kind: "namespace",
    operation: "Client.learningAttempts.abandon",
  },
  "HarnessService.GenerateDreamPlan": {
    kind: "namespace",
    operation: "Client.dreamPlans.generate",
  },
  "HarnessService.DecideDreamPlan": { kind: "namespace", operation: "Client.dreamPlans.decide" },
  "HarnessService.ListLearningProposals": {
    kind: "namespace",
    operation: "Client.learningProposals.list",
  },
  "HarnessService.GetLearningProposal": {
    kind: "namespace",
    operation: "Client.learningProposals.get",
  },
  "HarnessService.DecideLearningProposal": {
    kind: "namespace",
    operation: "Client.learningProposals.decide",
  },
  "HarnessService.UndoLearningPromotion": {
    kind: "namespace",
    operation: "Client.learningProposals.undoPromotion",
  },
  "HarnessService.ListLearnedSkills": { kind: "namespace", operation: "Client.learnedSkills.list" },
  "HarnessService.GetLearnedSkill": { kind: "namespace", operation: "Client.learnedSkills.get" },
  "HarnessService.DiffLearnedSkillVersions": {
    kind: "namespace",
    operation: "Client.learnedSkills.diffVersions",
  },
  "HarnessService.ActivateLearnedSkill": {
    kind: "namespace",
    operation: "Client.learnedSkills.activate",
  },
  "HarnessService.RejectLearnedSkill": {
    kind: "namespace",
    operation: "Client.learnedSkills.reject",
  },
  "HarnessService.ArchiveLearnedSkill": {
    kind: "namespace",
    operation: "Client.learnedSkills.archive",
  },
  "HarnessService.RollbackLearnedSkill": {
    kind: "namespace",
    operation: "Client.learnedSkills.rollback",
  },
  "HarnessService.ListSkillChanges": {
    kind: "namespace",
    operation: "Client.learnedSkills.listChanges",
  },
  "HarnessService.ListModels": { kind: "namespace", operation: "Client.models.list" },
  "HarnessService.CreateTeam": { kind: "namespace", operation: "Client.teams.create" },
  "HarnessService.SpawnTeammate": { kind: "lifecycle", operation: "Team.spawn" },
  "HarnessService.SendTeammateMessage": { kind: "lifecycle", operation: "Team.message" },
  "HarnessService.CancelTeammate": { kind: "lifecycle", operation: "Team.cancel" },
  "HarnessService.RunTeam": { kind: "lifecycle", operation: "Team.run" },
  "HarnessService.ListTeam": { kind: "lifecycle", operation: "Team.list" },
  "HarnessService.CleanupTeam": { kind: "lifecycle", operation: "Team.cleanup" },
  "HarnessService.ApprovePlan": { kind: "session", operation: "Session.resolvePlan" },
  "HarnessService.GetMcpAuthorizationPresentation": {
    kind: "lifecycle",
    operation: "McpAuthorization.presentation",
  },
  "HarnessService.RecheckMcpAuthorization": {
    kind: "lifecycle",
    operation: "McpAuthorization.recheck",
  },
  "HarnessService.CancelMcpAuthorization": {
    kind: "lifecycle",
    operation: "McpAuthorization.cancel",
  },
  "HarnessService.ListSessionMcpConnectors": {
    kind: "session",
    operation: "Session.listMcpConnectors",
  },
  "HarnessService.ConnectWorkspaceServices": {
    kind: "session",
    operation: "Session.connectWorkspaceServices",
  },
  "HarnessService.RetryWorkspaceEnrollment": {
    kind: "session",
    operation: "Session.retryWorkspaceEnrollment",
  },
  "HarnessService.CancelWorkspaceEnrollment": {
    kind: "session",
    operation: "Session.cancelWorkspaceEnrollment",
  },
  "ScheduleService.CreateSchedule": { kind: "namespace", operation: "Client.schedules.create" },
  "ScheduleService.GetSchedule": { kind: "namespace", operation: "Client.schedules.get" },
  "ScheduleService.ListSchedules": { kind: "namespace", operation: "Client.schedules.list" },
  "ScheduleService.UpdateSchedule": { kind: "namespace", operation: "Client.schedules.update" },
  "ScheduleService.DeleteSchedule": { kind: "namespace", operation: "Client.schedules.delete" },
  "ScheduleService.FireNow": { kind: "namespace", operation: "Client.schedules.fireNow" },
  "ScheduleService.PauseSchedule": { kind: "namespace", operation: "Client.schedules.pause" },
  "ScheduleService.ResumeSchedule": { kind: "namespace", operation: "Client.schedules.resume" },
  "ScheduleService.GetFire": { kind: "namespace", operation: "Client.schedules.getFire" },
  "ScheduleService.ListFires": { kind: "namespace", operation: "Client.schedules.listFires" },
} as const satisfies Record<string, Classification>;

const packageRoot = fileURLToPath(new URL("../", import.meta.url));
const sourceFiles = readdirSync(join(packageRoot, "src"), { withFileTypes: true })
  .filter((entry) => entry.isFile() && entry.name.endsWith(".ts") && !entry.name.endsWith(".d.ts"))
  .map((entry) => entry.name);

function referenceKey(node: ts.Node): string | undefined {
  if (
    ts.isElementAccessExpression(node) &&
    ts.isIdentifier(node.expression) &&
    node.expression.text === "RPC_CATALOG" &&
    ts.isStringLiteral(node.argumentExpression)
  ) {
    return node.argumentExpression.text;
  }
  if (
    ts.isPropertyAccessExpression(node) &&
    ts.isPropertyAccessExpression(node.expression) &&
    ts.isIdentifier(node.expression.expression) &&
    node.expression.expression.text === "HarnessService" &&
    node.expression.name.text === "method"
  ) {
    const name = node.name.text;
    return `HarnessService.${name[0]?.toUpperCase()}${name.slice(1)}`;
  }
  return undefined;
}

function isDescriptorCall(node: ts.Node): boolean {
  for (let parent = node.parent; parent !== undefined; parent = parent.parent) {
    if (!ts.isCallExpression(parent)) continue;
    const descriptor = parent.arguments[0];
    return (
      descriptor !== undefined &&
      descriptor.pos <= node.pos &&
      node.end <= descriptor.end &&
      ts.isPropertyAccessExpression(parent.expression) &&
      ["unary", "stream", "#unary", "#stream"].includes(parent.expression.name.text)
    );
  }
  return false;
}

function invocationPath(node: ts.Node, file: string): PublicOperation | undefined {
  let className = "";
  let methodName = "";
  let functionName = "";
  const properties: string[] = [];
  for (let parent = node.parent; parent !== undefined; parent = parent.parent) {
    if (ts.isClassDeclaration(parent)) className = parent.name?.text ?? "";
    if (ts.isMethodDeclaration(parent) && methodName === "") methodName = parent.name.getText();
    if (ts.isFunctionDeclaration(parent) && functionName === "") {
      functionName = parent.name?.text ?? "";
    }
    if (ts.isPropertyAssignment(parent)) {
      properties.unshift(parent.name.getText().replaceAll(/["']/gu, ""));
    }
  }

  if (file === "namespaces-core.ts" || file === "namespaces-ops.ts") {
    if (!new Set(["createCoreNamespaces", "createOperationalNamespaces"]).has(functionName)) {
      return undefined;
    }
    const [namespace, method] = properties;
    if (namespace === undefined || method === undefined) return undefined;
    return `Client.${namespace === "sessionInventory" ? "sessions" : namespace}.${method}` as PublicOperation;
  }
  if (file === "client.ts") {
    if (className === "SessionImpl") {
      return (
        methodName === "#startRun" ? "Session.run" : `Session.${methodName}`
      ) as PublicOperation;
    }
    if (className === "ClientImpl") {
      const member = properties.at(-1);
      if (member === "create" || member === "fork" || member === "get") {
        return `Client.sessions.${member}`;
      }
    }
  }
  if (file === "mcp-authorization.ts") {
    if (className === "McpAuthorizationImpl" && methodName === "presentation") {
      return "McpAuthorization.presentation";
    }
  }
  if (file === "plan.ts" && functionName === "createPlanResolution") return "Session.resolvePlan";
  if (file === "run-controls.ts" && className === "RunControlsImpl") {
    return `RunControls.${methodName}` as PublicOperation;
  }
  if (file === "server.ts" && functionName === "createServer" && properties[0] === "info") {
    return "Client.server.info";
  }
  if (file === "team.ts") {
    if (className === "TeamImpl") return `Team.${methodName}` as PublicOperation;
    if (functionName === "createTeams" && properties[0] === "create") {
      return "Client.teams.create";
    }
  }
  return undefined;
}

function authorizationFlowKeys(source: ts.SourceFile): readonly string[] {
  const flow = source.statements.find(
    (statement): statement is ts.ClassDeclaration =>
      ts.isClassDeclaration(statement) && statement.name?.text === "McpAuthorizationFlowImpl",
  );
  const start = flow?.members.find(
    (member): member is ts.MethodDeclaration =>
      ts.isMethodDeclaration(member) && member.name.getText(source) === "#start",
  );
  if (start?.body === undefined) return [];

  let selected: ts.ConditionalExpression | undefined;
  let streamed = false;
  const visit = (node: ts.Node): void => {
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.name.text === "method" &&
      node.initializer !== undefined &&
      ts.isConditionalExpression(node.initializer)
    ) {
      selected = node.initializer;
    }
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression) &&
      node.expression.name.text === "stream" &&
      node.arguments[0] !== undefined &&
      ts.isAsExpression(node.arguments[0]) &&
      ts.isIdentifier(node.arguments[0].expression) &&
      node.arguments[0].expression.text === "method"
    ) {
      streamed = true;
    }
    ts.forEachChild(node, visit);
  };
  visit(start.body);
  if (!streamed || selected === undefined) return [];
  const condition = selected.condition;
  if (
    !ts.isBinaryExpression(condition) ||
    condition.operatorToken.kind !== ts.SyntaxKind.EqualsEqualsEqualsToken ||
    !ts.isPropertyAccessExpression(condition.left) ||
    condition.left.expression.kind !== ts.SyntaxKind.ThisKeyword ||
    condition.left.name.text !== "operation" ||
    !ts.isStringLiteral(condition.right) ||
    condition.right.text !== "recheck"
  ) {
    return [];
  }
  const keys = [referenceKey(selected.whenTrue), referenceKey(selected.whenFalse)];
  return keys[0] === "HarnessService.RecheckMcpAuthorization" &&
    keys[1] === "HarnessService.CancelMcpAuthorization"
    ? (keys as [string, string])
    : [];
}

interface SourceWitnesses {
  readonly operations: ReadonlyMap<string, ReadonlySet<PublicOperation>>;
  readonly invoked: ReadonlySet<string>;
}

function sourceWitnesses(): SourceWitnesses {
  const operations = new Map<string, Set<PublicOperation>>();
  const invoked = new Set<string>();
  const record = (key: string, operation: PublicOperation): void => {
    const entries = operations.get(key) ?? new Set<PublicOperation>();
    entries.add(operation);
    operations.set(key, entries);
  };
  for (const name of sourceFiles) {
    const source = ts.createSourceFile(
      name,
      readFileSync(join(packageRoot, "src", name), "utf8"),
      ts.ScriptTarget.Latest,
      true,
    );
    const visit = (node: ts.Node): void => {
      const key = referenceKey(node);
      if (key !== undefined && isDescriptorCall(node)) {
        invoked.add(key);
        const operation = invocationPath(node, name);
        if (operation !== undefined) record(key, operation);
      }
      ts.forEachChild(node, visit);
    };
    visit(source);
    if (name === "mcp-authorization.ts") {
      for (const key of authorizationFlowKeys(source)) {
        invoked.add(key);
        record(
          key,
          key === "HarnessService.RecheckMcpAuthorization"
            ? "McpAuthorization.recheck"
            : "McpAuthorization.cancel",
        );
      }
    }
  }
  return { operations, invoked };
}

describe("SDK high-level RPC surface", () => {
  it("RPC classifications exactly cover public descriptors", () => {
    const descriptors = [
      ...Object.values(HarnessService.method).map((method) => `HarnessService.${method.name}`),
      ...Object.values(ScheduleService.method).map((method) => `ScheduleService.${method.name}`),
    ];
    expect(new Set(descriptors).size).toBe(descriptors.length);
    expect(Object.keys(HIGH_LEVEL_SURFACE).sort()).toEqual(descriptors.sort());
    expect(Object.keys(RPC_CATALOG).sort()).toEqual(descriptors.sort());
  });

  it("covered RPCs have public invocation paths", () => {
    const { operations } = sourceWitnesses();
    for (const [key, row] of Object.entries(HIGH_LEVEL_SURFACE)) {
      if (row.kind === "raw-only") continue;
      // These indirect public paths are exercised below against a recording transport.
      if (
        key === "HarnessService.GetCompatibilityInfo" ||
        key === "HarnessService.WatchSessionEvents"
      ) {
        continue;
      }
      expect(operations.get(key), `${key} must be invoked by ${row.operation}`).toContain(
        row.operation,
      );
      expect(row.kind === "session" ? row.operation.startsWith("Session.") : true).toBe(true);
    }
  });

  it("a descriptor read without a unary or stream call is not an invocation", () => {
    const source = ts.createSourceFile(
      "dead-reference.ts",
      `void RPC_CATALOG["HarnessService.ListAgents"];
       operations.unary(RPC_CATALOG["HarnessService.ListAgents"].grpc.descriptor, {}, {});`,
      ts.ScriptTarget.Latest,
      true,
    );
    const calls: boolean[] = [];
    const visit = (node: ts.Node): void => {
      if (referenceKey(node) === "HarnessService.ListAgents") calls.push(isDescriptorCall(node));
      ts.forEachChild(node, visit);
    };
    visit(source);
    expect(calls).toEqual([false, true]);
  });

  it("public server compatibility refresh invokes GetCompatibilityInfo", async () => {
    expect(HIGH_LEVEL_SURFACE["HarnessService.GetCompatibilityInfo"]).toEqual({
      kind: "namespace",
      operation: "Client.server.compatibility",
    });
    let calls = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => {
          calls += 1;
          return { apiMajor: 1, capabilities: {}, features: ["server_info"] };
        },
      });
    });
    const client = connect({ transport });
    try {
      await client.server.compatibility();
      const before = calls;
      await client.server.compatibility();
      expect(calls).toBe(before + 1);
    } finally {
      await client.close();
    }
  });

  it("public session activity invokes WatchSessionEvents", async () => {
    expect(HIGH_LEVEL_SURFACE["HarnessService.WatchSessionEvents"]).toEqual({
      kind: "session",
      operation: "Session.activity",
    });
    const watched: string[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: {},
          features: ["watch_session_events"],
        }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents: async function* (request) {
          watched.push(request.sessionId);
          yield {
            cursor: "cursor-1",
            event: { runId: "run-1", text: "working", type: "message.delta" },
            phase: "replay",
          };
        },
      });
    });
    const client = connect({ transport });
    try {
      const session = await client.sessions.get("covered-session");
      const activity = await session.activity();
      try {
        const first = await activity[Symbol.asyncIterator]().next();
        expect(first.done).toBe(false);
        expect(watched).toEqual(["covered-session"]);
      } finally {
        await activity.close();
      }
    } finally {
      await client.close();
    }
  });

  it("raw-only RPC exceptions are exact and reasoned", () => {
    const { invoked } = sourceWitnesses();
    const raw = Object.entries(HIGH_LEVEL_SURFACE).flatMap(([key, row]) =>
      row.kind === "raw-only" ? [key] : [],
    );
    expect(raw.sort()).toEqual([
      "HarnessService.StreamSessionEvents",
      "HarnessService.StreamSessionLive",
    ]);
    for (const key of raw) {
      const row = HIGH_LEVEL_SURFACE[key as keyof typeof HIGH_LEVEL_SURFACE];
      expect(row.kind).toBe("raw-only");
      if (row.kind === "raw-only") expect(row.rationale.trim().length).toBeGreaterThan(20);
      expect(invoked.has(key), `${key} must remain unused by high-level source`).toBe(false);
    }
  });
});
