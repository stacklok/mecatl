import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import {
  create,
  type DescMessage,
  type DescMethodStreaming,
  type DescMethodUnary,
  type MessageInitShape,
  type MessageShape,
} from "@bufbuild/protobuf";
import type { ContextValues, StreamResponse, Transport, UnaryResponse } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { connect } from "../src/index.js";
import { RPC_CATALOG } from "../src/rpc-catalog.js";

const packageRoot = fileURLToPath(new URL("../", import.meta.url));
const apiReports = ["mecatl-sdk.api.md", "mecatl-sdk-node.api.md"] as const;
const signatureAllowlist = new Set(["Promise", "RequestOptions"]);
const batchBCatalogKeys = [
  "HarnessService.ListSessions",
  "HarnessService.GetStorageHealth",
  "HarnessService.PlanSessionMigration",
  "HarnessService.ApplySessionMigration",
  "HarnessService.ResumeSessionMigration",
  "HarnessService.CancelSessionMigration",
  "HarnessService.GetSessionMigrationJob",
  "HarnessService.PlanSessionCleanup",
  "HarnessService.ApplySessionCleanup",
  "HarnessService.CancelSessionCleanup",
  "HarnessService.GetSessionCleanupJob",
  "HarnessService.ListSkills",
  "HarnessService.GetSoul",
  "HarnessService.GetUserModel",
  "HarnessService.ReflectSession",
  "HarnessService.GetLearningAttempt",
  "HarnessService.ListLearningAttempts",
  "HarnessService.RetryLearningAttempt",
  "HarnessService.AbandonLearningAttempt",
  "HarnessService.GenerateDreamPlan",
  "HarnessService.DecideDreamPlan",
  "HarnessService.ListLearningProposals",
  "HarnessService.GetLearningProposal",
  "HarnessService.DecideLearningProposal",
  "HarnessService.UndoLearningPromotion",
  "HarnessService.ListLearnedSkills",
  "HarnessService.GetLearnedSkill",
  "HarnessService.DiffLearnedSkillVersions",
  "HarnessService.ActivateLearnedSkill",
  "HarnessService.RejectLearnedSkill",
  "HarnessService.ArchiveLearnedSkill",
  "HarnessService.RollbackLearnedSkill",
  "HarnessService.ListSkillChanges",
  "ScheduleService.CreateSchedule",
  "ScheduleService.GetSchedule",
  "ScheduleService.ListSchedules",
  "ScheduleService.UpdateSchedule",
  "ScheduleService.DeleteSchedule",
  "ScheduleService.FireNow",
  "ScheduleService.PauseSchedule",
  "ScheduleService.ResumeSchedule",
  "ScheduleService.GetFire",
  "ScheduleService.ListFires",
] as const;
const generatedSignatureTypes = new Set(
  batchBCatalogKeys.flatMap((key) => {
    const descriptor = RPC_CATALOG[key].grpc.descriptor;
    return [
      descriptor.input.typeName.split(".").at(-1),
      descriptor.output.typeName.split(".").at(-1),
    ];
  }),
);

const expectedSignatures = {
  DreamPlans: [
    "decide(request: DecideDreamPlanRequest, options?: RequestOptions): Promise<DecideDreamPlanResponse>;",
    "generate(request: GenerateDreamPlanRequest, options?: RequestOptions): Promise<GenerateDreamPlanResponse>;",
  ],
  LearnedSkills: [
    "activate(request: MutateLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;",
    "archive(request: MutateLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;",
    "diffVersions(request: DiffLearnedSkillVersionsRequest, options?: RequestOptions): Promise<DiffLearnedSkillVersionsResponse>;",
    "get(request: GetLearnedSkillRequest, options?: RequestOptions): Promise<GetLearnedSkillResponse>;",
    "list(request: ListLearnedSkillsRequest, options?: RequestOptions): Promise<ListLearnedSkillsResponse>;",
    "listChanges(request: ListSkillChangesRequest, options?: RequestOptions): Promise<ListSkillChangesResponse>;",
    "reject(request: MutateLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;",
    "rollback(request: RollbackLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;",
  ],
  LearningAttempts: [
    "abandon(request: MutateLearningAttemptRequest, options?: RequestOptions): Promise<MutateLearningAttemptResponse>;",
    "get(request: GetLearningAttemptRequest, options?: RequestOptions): Promise<GetLearningAttemptResponse>;",
    "list(request: ListLearningAttemptsRequest, options?: RequestOptions): Promise<ListLearningAttemptsResponse>;",
    "retry(request: MutateLearningAttemptRequest, options?: RequestOptions): Promise<MutateLearningAttemptResponse>;",
  ],
  LearningProposals: [
    "decide(request: DecideLearningProposalRequest, options?: RequestOptions): Promise<DecideLearningProposalResponse>;",
    "get(request: GetLearningProposalRequest, options?: RequestOptions): Promise<GetLearningProposalResponse>;",
    "list(request: ListLearningProposalsRequest, options?: RequestOptions): Promise<ListLearningProposalsResponse>;",
    "undoPromotion(request: UndoLearningPromotionRequest, options?: RequestOptions): Promise<UndoLearningPromotionResponse>;",
  ],
  Reflection: [
    "reflect(request: ReflectSessionRequest, options?: RequestOptions): Promise<ReflectSessionResponse>;",
  ],
  Schedules: [
    "create(request: CreateScheduleRequest, options?: RequestOptions): Promise<CreateScheduleResponse>;",
    "delete(request: DeleteScheduleRequest, options?: RequestOptions): Promise<DeleteScheduleResponse>;",
    "fireNow(request: FireNowRequest, options?: RequestOptions): Promise<FireNowResponse>;",
    "get(request: GetScheduleRequest, options?: RequestOptions): Promise<GetScheduleResponse>;",
    "getFire(request: GetFireRequest, options?: RequestOptions): Promise<GetFireResponse>;",
    "list(request: ListSchedulesRequest, options?: RequestOptions): Promise<ListSchedulesResponse>;",
    "listFires(request: ListFiresRequest, options?: RequestOptions): Promise<ListFiresResponse>;",
    "pause(request: PauseScheduleRequest, options?: RequestOptions): Promise<PauseScheduleResponse>;",
    "resume(request: ResumeScheduleRequest, options?: RequestOptions): Promise<ResumeScheduleResponse>;",
    "update(request: UpdateScheduleRequest, options?: RequestOptions): Promise<UpdateScheduleResponse>;",
  ],
  Skills: [
    "list(request: ListSkillsRequest, options?: RequestOptions): Promise<ListSkillsResponse>;",
  ],
  Soul: ["get(request: GetSoulRequest, options?: RequestOptions): Promise<GetSoulResponse>;"],
  Storage: [
    "applyCleanup(request: ApplySessionCleanupRequest, options?: RequestOptions): Promise<CleanupJob>;",
    "applyMigration(request: ApplySessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationJob>;",
    "cancelCleanup(request: CancelSessionCleanupRequest, options?: RequestOptions): Promise<CleanupJob>;",
    "cancelMigration(request: CancelSessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationJob>;",
    "getCleanupJob(request: GetSessionCleanupJobRequest, options?: RequestOptions): Promise<CleanupJob>;",
    "getHealth(request: GetStorageHealthRequest, options?: RequestOptions): Promise<GetStorageHealthResponse>;",
    "getMigrationJob(request: GetSessionMigrationJobRequest, options?: RequestOptions): Promise<SessionMigrationJob>;",
    "planCleanup(request: PlanSessionCleanupRequest, options?: RequestOptions): Promise<PlanSessionCleanupResponse>;",
    "planMigration(request: PlanSessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationPlan>;",
    "resumeMigration(request: ResumeSessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationJob>;",
  ],
  UserModel: [
    "get(request: GetUserModelRequest, options?: RequestOptions): Promise<GetUserModelResponse>;",
  ],
} as const;

function interfaceMethodSignatures(report: string, name: string): string[] {
  const alias = new RegExp(`export \\{ ([A-Za-z0-9_]+) as ${name} \\}`, "u").exec(report)?.[1];
  const reportName = alias ?? name;
  const body = new RegExp(`(?:export )?interface ${reportName} \\{([\\s\\S]*?)\\n\\}`, "u").exec(
    report,
  )?.[1];
  if (body === undefined) throw new Error(`API report is missing ${name}`);
  return body
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => /^[a-z][A-Za-z0-9]*\(/u.test(line));
}

function assertGeneratedTypeSignatures(report: string, reportName: string): void {
  for (const namespace of Object.keys(expectedSignatures)) {
    for (const signature of interfaceMethodSignatures(report, namespace)) {
      const typeNames = signature.match(/\b[A-Z][A-Za-z0-9]*\b/gu) ?? [];
      const disallowed = typeNames.filter(
        (name) => !generatedSignatureTypes.has(name) && !signatureAllowlist.has(name),
      );
      expect(disallowed, `${reportName}:${namespace}.${signature}`).toEqual([]);
    }
  }

  const sessionList = interfaceMethodSignatures(report, "Sessions").filter((signature) =>
    signature.startsWith("list("),
  );
  expect(sessionList).toEqual([
    "list(request: ListSessionsRequest, options?: RequestOptions): Promise<ListSessionsResponse>;",
  ]);
  for (const signature of sessionList) {
    const typeNames = signature.match(/\b[A-Z][A-Za-z0-9]*\b/gu) ?? [];
    expect(
      typeNames.filter(
        (name) => !generatedSignatureTypes.has(name) && !signatureAllowlist.has(name),
      ),
      `${reportName}:Sessions.${signature}`,
    ).toEqual([]);
  }
}

class OperationalTransport implements Transport {
  readonly calls: string[] = [];
  readonly responses = new Map<string, unknown>();

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    _signal: AbortSignal | undefined,
    _timeoutMs: number | undefined,
    _header: HeadersInit | undefined,
    _input: MessageInitShape<I>,
    _contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    this.calls.push(method.name);
    const responseInit = (value: object): MessageInitShape<O> =>
      value as unknown as MessageInitShape<O>;
    let init = responseInit({});
    switch (method.name) {
      case "GetCompatibilityInfo":
        init = responseInit({ apiMajor: 1 });
        break;
      case "GetSchedule":
        init = responseInit({
          schedule: {
            spec: { misfire: 99 },
            state: { lastFireSessionId: "optional-fire-session" },
          },
        });
        break;
      case "FireNow":
        init = responseInit({ fireId: "fire-1", sessionId: "fire-1" });
        break;
      case "GetFire":
        init = responseInit({
          fire: { id: "fire-1", scheduleName: "nightly", sessionId: "fire-1" },
        });
        break;
      case "ListFires":
        init = responseInit({
          fires: [{ id: "fire-2", scheduleName: "nightly", sessionId: "fire-2" }],
        });
        break;
      case "PlanSessionMigration":
        init = responseInit({ available: true, planId: "migration-plan" });
        break;
      case "ApplySessionMigration":
      case "ResumeSessionMigration":
      case "CancelSessionMigration":
      case "GetSessionMigrationJob":
        init = responseInit({ jobId: method.name, state: "future_migration_state" });
        break;
      case "PlanSessionCleanup":
        init = responseInit({
          confirmationToken: "cleanup-token",
          eligibleCounts: { total: 2 },
          plannedJobId: "cleanup-plan",
        });
        break;
      case "ApplySessionCleanup":
      case "CancelSessionCleanup":
      case "GetSessionCleanupJob":
        init = responseInit({ jobId: method.name, state: "future_cleanup_state" });
        break;
    }
    const message = create(method.output, init);
    this.responses.set(method.name, message);
    return {
      header: new Headers(),
      message,
      method,
      service: method.parent,
      stream: false,
      trailer: new Headers(),
    };
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    _method: DescMethodStreaming<I, O>,
    _signal: AbortSignal | undefined,
    _timeoutMs: number | undefined,
    _header: HeadersInit | undefined,
    _input: AsyncIterable<MessageInitShape<I>>,
    _contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    throw new Error("operational namespaces are unary");
  }
}

type BatchInput<Key extends (typeof batchBCatalogKeys)[number]> =
  (typeof RPC_CATALOG)[Key]["grpc"]["descriptor"] extends DescMethodUnary<infer Input, DescMessage>
    ? Input
    : never;

function request<Key extends (typeof batchBCatalogKeys)[number]>(
  key: Key,
): MessageShape<BatchInput<Key>> {
  return create(RPC_CATALOG[key].grpc.descriptor.input) as MessageShape<BatchInput<Key>>;
}

describe("operational typed namespaces", () => {
  it("operational namespaces are thin generated-type wrappers", async () => {
    for (const reportName of apiReports) {
      const report = readFileSync(`${packageRoot}/etc/${reportName}`, "utf8");
      for (const [namespace, signatures] of Object.entries(expectedSignatures)) {
        expect(
          interfaceMethodSignatures(report, namespace).sort(),
          `${reportName}:${namespace}`,
        ).toEqual([...signatures].sort());
      }
      assertGeneratedTypeSignatures(report, reportName);
    }

    const transport = new OperationalTransport();
    const client = connect({ transport });
    const calls = [
      () => client.sessions.list(request("HarnessService.ListSessions")),
      () => client.storage.getHealth(request("HarnessService.GetStorageHealth")),
      () => client.storage.planMigration(request("HarnessService.PlanSessionMigration")),
      () => client.storage.applyMigration(request("HarnessService.ApplySessionMigration")),
      () => client.storage.resumeMigration(request("HarnessService.ResumeSessionMigration")),
      () => client.storage.cancelMigration(request("HarnessService.CancelSessionMigration")),
      () => client.storage.getMigrationJob(request("HarnessService.GetSessionMigrationJob")),
      () => client.storage.planCleanup(request("HarnessService.PlanSessionCleanup")),
      () => client.storage.applyCleanup(request("HarnessService.ApplySessionCleanup")),
      () => client.storage.cancelCleanup(request("HarnessService.CancelSessionCleanup")),
      () => client.storage.getCleanupJob(request("HarnessService.GetSessionCleanupJob")),
      () => client.skills.list(request("HarnessService.ListSkills")),
      () => client.soul.get(request("HarnessService.GetSoul")),
      () => client.userModel.get(request("HarnessService.GetUserModel")),
      () => client.reflection.reflect(request("HarnessService.ReflectSession")),
      () => client.learningAttempts.get(request("HarnessService.GetLearningAttempt")),
      () => client.learningAttempts.list(request("HarnessService.ListLearningAttempts")),
      () => client.learningAttempts.retry(request("HarnessService.RetryLearningAttempt")),
      () => client.learningAttempts.abandon(request("HarnessService.AbandonLearningAttempt")),
      () => client.dreamPlans.generate(request("HarnessService.GenerateDreamPlan")),
      () => client.dreamPlans.decide(request("HarnessService.DecideDreamPlan")),
      () => client.learningProposals.list(request("HarnessService.ListLearningProposals")),
      () => client.learningProposals.get(request("HarnessService.GetLearningProposal")),
      () => client.learningProposals.decide(request("HarnessService.DecideLearningProposal")),
      () => client.learningProposals.undoPromotion(request("HarnessService.UndoLearningPromotion")),
      () => client.learnedSkills.list(request("HarnessService.ListLearnedSkills")),
      () => client.learnedSkills.get(request("HarnessService.GetLearnedSkill")),
      () => client.learnedSkills.diffVersions(request("HarnessService.DiffLearnedSkillVersions")),
      () => client.learnedSkills.activate(request("HarnessService.ActivateLearnedSkill")),
      () => client.learnedSkills.reject(request("HarnessService.RejectLearnedSkill")),
      () => client.learnedSkills.archive(request("HarnessService.ArchiveLearnedSkill")),
      () => client.learnedSkills.rollback(request("HarnessService.RollbackLearnedSkill")),
      () => client.learnedSkills.listChanges(request("HarnessService.ListSkillChanges")),
      () => client.schedules.create(request("ScheduleService.CreateSchedule")),
      () => client.schedules.get(request("ScheduleService.GetSchedule")),
      () => client.schedules.list(request("ScheduleService.ListSchedules")),
      () => client.schedules.update(request("ScheduleService.UpdateSchedule")),
      () => client.schedules.delete(request("ScheduleService.DeleteSchedule")),
      () => client.schedules.fireNow(request("ScheduleService.FireNow")),
      () => client.schedules.pause(request("ScheduleService.PauseSchedule")),
      () => client.schedules.resume(request("ScheduleService.ResumeSchedule")),
      () => client.schedules.getFire(request("ScheduleService.GetFire")),
      () => client.schedules.listFires(request("ScheduleService.ListFires")),
    ];
    for (const call of calls) await call();

    expect(transport.calls.filter((method) => method !== "GetCompatibilityInfo")).toEqual(
      batchBCatalogKeys.map((key) => RPC_CATALOG[key].method),
    );
    await client.close();
  });

  it("schedule fire and storage mutations preserve exact generated responses", async () => {
    const transport = new OperationalTransport();
    const client = connect({ transport });

    const schedule = await client.schedules.get(request("ScheduleService.GetSchedule"));
    const fireNow = await client.schedules.fireNow(request("ScheduleService.FireNow"));
    const fire = await client.schedules.getFire(request("ScheduleService.GetFire"));
    const fires = await client.schedules.listFires(request("ScheduleService.ListFires"));
    expect(schedule).toBe(transport.responses.get("GetSchedule"));
    expect(schedule.schedule?.spec?.misfire).toBe(99);
    expect(schedule.schedule?.state?.lastFireSessionId).toBe("optional-fire-session");
    expect(fireNow).toBe(transport.responses.get("FireNow"));
    expect(fire).toBe(transport.responses.get("GetFire"));
    expect(fire.fire?.sessionId).toBe("fire-1");
    expect(fires).toBe(transport.responses.get("ListFires"));
    expect(fires.fires[0]?.sessionId).toBe("fire-2");

    const storageCalls = [
      [
        "PlanSessionMigration",
        client.storage.planMigration(request("HarnessService.PlanSessionMigration")),
      ],
      [
        "ApplySessionMigration",
        client.storage.applyMigration(request("HarnessService.ApplySessionMigration")),
      ],
      [
        "ResumeSessionMigration",
        client.storage.resumeMigration(request("HarnessService.ResumeSessionMigration")),
      ],
      [
        "CancelSessionMigration",
        client.storage.cancelMigration(request("HarnessService.CancelSessionMigration")),
      ],
      [
        "GetSessionMigrationJob",
        client.storage.getMigrationJob(request("HarnessService.GetSessionMigrationJob")),
      ],
      [
        "PlanSessionCleanup",
        client.storage.planCleanup(request("HarnessService.PlanSessionCleanup")),
      ],
      [
        "ApplySessionCleanup",
        client.storage.applyCleanup(request("HarnessService.ApplySessionCleanup")),
      ],
      [
        "CancelSessionCleanup",
        client.storage.cancelCleanup(request("HarnessService.CancelSessionCleanup")),
      ],
      [
        "GetSessionCleanupJob",
        client.storage.getCleanupJob(request("HarnessService.GetSessionCleanupJob")),
      ],
    ] as const;
    for (const [method, pending] of storageCalls) {
      expect(await pending).toBe(transport.responses.get(method));
    }
    expect((await storageCalls[1][1]).state).toBe("future_migration_state");
    expect((await storageCalls[5][1]).eligibleCounts?.total).toBe(2);
    expect((await storageCalls[6][1]).state).toBe("future_cleanup_state");
    await client.close();
  });

  it("the operational namespace batch adds no bespoke wire types", () => {
    for (const reportName of apiReports) {
      assertGeneratedTypeSignatures(
        readFileSync(`${packageRoot}/etc/${reportName}`, "utf8"),
        reportName,
      );
    }
  });
});
