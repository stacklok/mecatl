import type {
  ApplySessionCleanupRequest,
  CancelSessionCleanupRequest,
  CleanupJob,
  DecideDreamPlanRequest,
  DecideDreamPlanResponse,
  DecideLearningProposalRequest,
  DecideLearningProposalResponse,
  DiffLearnedSkillVersionsRequest,
  DiffLearnedSkillVersionsResponse,
  GenerateDreamPlanRequest,
  GenerateDreamPlanResponse,
  GetLearnedSkillRequest,
  GetLearnedSkillResponse,
  GetLearningAttemptRequest,
  GetLearningAttemptResponse,
  GetLearningProposalRequest,
  GetLearningProposalResponse,
  GetSessionCleanupJobRequest,
  GetSoulRequest,
  GetSoulResponse,
  GetStorageHealthRequest,
  GetStorageHealthResponse,
  GetUserModelRequest,
  GetUserModelResponse,
  ListLearnedSkillsRequest,
  ListLearnedSkillsResponse,
  ListLearningAttemptsRequest,
  ListLearningAttemptsResponse,
  ListLearningProposalsRequest,
  ListLearningProposalsResponse,
  ListSessionsRequest,
  ListSessionsResponse,
  ListSkillChangesRequest,
  ListSkillChangesResponse,
  ListSkillsRequest,
  ListSkillsResponse,
  MutateLearnedSkillRequest,
  MutateLearnedSkillResponse,
  MutateLearningAttemptRequest,
  MutateLearningAttemptResponse,
  PlanSessionCleanupRequest,
  PlanSessionCleanupResponse,
  ReflectSessionRequest,
  ReflectSessionResponse,
  RollbackLearnedSkillRequest,
  UndoLearningPromotionRequest,
  UndoLearningPromotionResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import type {
  CreateScheduleRequest,
  CreateScheduleResponse,
  DeleteScheduleRequest,
  DeleteScheduleResponse,
  FireNowRequest,
  FireNowResponse,
  GetFireRequest,
  GetFireResponse,
  GetScheduleRequest,
  GetScheduleResponse,
  ListFiresRequest,
  ListFiresResponse,
  ListSchedulesRequest,
  ListSchedulesResponse,
  PauseScheduleRequest,
  PauseScheduleResponse,
  ResumeScheduleRequest,
  ResumeScheduleResponse,
  UpdateScheduleRequest,
  UpdateScheduleResponse,
} from "./gen/mecatl/v1/schedule_pb.js";
import type { RequestOptions } from "./namespaces-core.js";
import type { RawClient } from "./raw.js";
import { RPC_CATALOG } from "./rpc-catalog.js";

/** Configured skill inventory operations. @public */
export interface Skills {
  /** Lists the configured skills visible to the server. */
  list(request: ListSkillsRequest, options?: RequestOptions): Promise<ListSkillsResponse>;
}

/** Learned-skill inventory and server-owned lifecycle operations. @public */
export interface LearnedSkills {
  /** Lists learned skills and their lifecycle state. */
  list(
    request: ListLearnedSkillsRequest,
    options?: RequestOptions,
  ): Promise<ListLearnedSkillsResponse>;
  /** Gets one learned skill. */
  get(request: GetLearnedSkillRequest, options?: RequestOptions): Promise<GetLearnedSkillResponse>;
  /** Compares two versions of a learned skill. */
  diffVersions(
    request: DiffLearnedSkillVersionsRequest,
    options?: RequestOptions,
  ): Promise<DiffLearnedSkillVersionsResponse>;
  /** Activates a learned skill. */
  activate(
    request: MutateLearnedSkillRequest,
    options?: RequestOptions,
  ): Promise<MutateLearnedSkillResponse>;
  /** Rejects a learned skill. */
  reject(
    request: MutateLearnedSkillRequest,
    options?: RequestOptions,
  ): Promise<MutateLearnedSkillResponse>;
  /** Archives a learned skill. */
  archive(
    request: MutateLearnedSkillRequest,
    options?: RequestOptions,
  ): Promise<MutateLearnedSkillResponse>;
  /** Rolls a learned skill back to an earlier version. */
  rollback(
    request: RollbackLearnedSkillRequest,
    options?: RequestOptions,
  ): Promise<MutateLearnedSkillResponse>;
  /** Lists the recorded changes to learned skills. */
  listChanges(
    request: ListSkillChangesRequest,
    options?: RequestOptions,
  ): Promise<ListSkillChangesResponse>;
}

/** Learning-attempt inventory and server-owned lifecycle operations. @public */
export interface LearningAttempts {
  /** Gets one learning attempt. */
  get(
    request: GetLearningAttemptRequest,
    options?: RequestOptions,
  ): Promise<GetLearningAttemptResponse>;
  /** Lists learning attempts visible to the caller. */
  list(
    request: ListLearningAttemptsRequest,
    options?: RequestOptions,
  ): Promise<ListLearningAttemptsResponse>;
  /** Retries a failed learning attempt. */
  retry(
    request: MutateLearningAttemptRequest,
    options?: RequestOptions,
  ): Promise<MutateLearningAttemptResponse>;
  /** Abandons an eligible learning attempt. */
  abandon(
    request: MutateLearningAttemptRequest,
    options?: RequestOptions,
  ): Promise<MutateLearningAttemptResponse>;
}

/** Learning-proposal inventory and server-owned decision operations. @public */
export interface LearningProposals {
  /** Lists staged learning proposals. */
  list(
    request: ListLearningProposalsRequest,
    options?: RequestOptions,
  ): Promise<ListLearningProposalsResponse>;
  /** Gets one staged learning proposal. */
  get(
    request: GetLearningProposalRequest,
    options?: RequestOptions,
  ): Promise<GetLearningProposalResponse>;
  /** Approves or rejects a staged learning proposal. */
  decide(
    request: DecideLearningProposalRequest,
    options?: RequestOptions,
  ): Promise<DecideLearningProposalResponse>;
  /** Reverts an eligible learning promotion. */
  undoPromotion(
    request: UndoLearningPromotionRequest,
    options?: RequestOptions,
  ): Promise<UndoLearningPromotionResponse>;
}

/** Session-reflection operations. @public */
export interface Reflection {
  /** Reflects one completed session into learning evidence. */
  reflect(
    request: ReflectSessionRequest,
    options?: RequestOptions,
  ): Promise<ReflectSessionResponse>;
}

/** Resolved soul inspection operations. @public */
export interface Soul {
  /** Gets the server's resolved soul snapshot. */
  get(request: GetSoulRequest, options?: RequestOptions): Promise<GetSoulResponse>;
}

/** Resolved user-model inspection operations. @public */
export interface UserModel {
  /** Gets the caller's bounded user-model index or one detail entry. */
  get(request: GetUserModelRequest, options?: RequestOptions): Promise<GetUserModelResponse>;
}

/** Dream-plan generation and server-owned decision operations. @public */
export interface DreamPlans {
  /** Generates a bounded-lifetime dream plan. */
  generate(
    request: GenerateDreamPlanRequest,
    options?: RequestOptions,
  ): Promise<GenerateDreamPlanResponse>;
  /** Applies or dismisses a generated dream plan. */
  decide(
    request: DecideDreamPlanRequest,
    options?: RequestOptions,
  ): Promise<DecideDreamPlanResponse>;
}

/** Schedule and fire inventory plus server-owned lifecycle operations. @public */
export interface Schedules {
  /** Creates a recurring schedule. */
  create(request: CreateScheduleRequest, options?: RequestOptions): Promise<CreateScheduleResponse>;
  /** Gets one schedule. */
  get(request: GetScheduleRequest, options?: RequestOptions): Promise<GetScheduleResponse>;
  /** Lists schedules visible to the caller. */
  list(request: ListSchedulesRequest, options?: RequestOptions): Promise<ListSchedulesResponse>;
  /** Updates one schedule. */
  update(request: UpdateScheduleRequest, options?: RequestOptions): Promise<UpdateScheduleResponse>;
  /** Deletes one schedule. */
  delete(request: DeleteScheduleRequest, options?: RequestOptions): Promise<DeleteScheduleResponse>;
  /** Requests an immediate schedule fire. */
  fireNow(request: FireNowRequest, options?: RequestOptions): Promise<FireNowResponse>;
  /** Pauses one schedule. */
  pause(request: PauseScheduleRequest, options?: RequestOptions): Promise<PauseScheduleResponse>;
  /** Resumes one paused schedule. */
  resume(request: ResumeScheduleRequest, options?: RequestOptions): Promise<ResumeScheduleResponse>;
  /** Gets one schedule fire. */
  getFire(request: GetFireRequest, options?: RequestOptions): Promise<GetFireResponse>;
  /** Lists fires for a schedule. */
  listFires(request: ListFiresRequest, options?: RequestOptions): Promise<ListFiresResponse>;
}

/** Storage health and cleanup operations owned by the server. @public */
export interface Storage {
  /** Gets the configured session-storage health. */
  getHealth(
    request: GetStorageHealthRequest,
    options?: RequestOptions,
  ): Promise<GetStorageHealthResponse>;
  /** Previews a session-storage cleanup. */
  planCleanup(
    request: PlanSessionCleanupRequest,
    options?: RequestOptions,
  ): Promise<PlanSessionCleanupResponse>;
  /** Starts a planned session-storage cleanup. */
  applyCleanup(request: ApplySessionCleanupRequest, options?: RequestOptions): Promise<CleanupJob>;
  /** Cancels a session-storage cleanup. */
  cancelCleanup(
    request: CancelSessionCleanupRequest,
    options?: RequestOptions,
  ): Promise<CleanupJob>;
  /** Gets one session-storage cleanup job. */
  getCleanupJob(
    request: GetSessionCleanupJobRequest,
    options?: RequestOptions,
  ): Promise<CleanupJob>;
}

interface SessionInventory {
  list(request: ListSessionsRequest, options?: RequestOptions): Promise<ListSessionsResponse>;
}

export interface OperationalNamespaces {
  readonly dreamPlans: DreamPlans;
  readonly learnedSkills: LearnedSkills;
  readonly learningAttempts: LearningAttempts;
  readonly learningProposals: LearningProposals;
  readonly reflection: Reflection;
  readonly schedules: Schedules;
  readonly sessionInventory: SessionInventory;
  readonly skills: Skills;
  readonly soul: Soul;
  readonly storage: Storage;
  readonly userModel: UserModel;
}

/** Internal construction seam that keeps every namespace on the client's raw operation path. */
export function createOperationalNamespaces(
  operations: Pick<RawClient, "unary">,
): OperationalNamespaces {
  return {
    dreamPlans: {
      decide: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.DecideDreamPlan"].grpc.descriptor,
          request,
          options,
        ),
      generate: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GenerateDreamPlan"].grpc.descriptor,
          request,
          options,
        ),
    },
    learnedSkills: {
      activate: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ActivateLearnedSkill"].grpc.descriptor,
          request,
          options,
        ),
      archive: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ArchiveLearnedSkill"].grpc.descriptor,
          request,
          options,
        ),
      diffVersions: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.DiffLearnedSkillVersions"].grpc.descriptor,
          request,
          options,
        ),
      get: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GetLearnedSkill"].grpc.descriptor,
          request,
          options,
        ),
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListLearnedSkills"].grpc.descriptor,
          request,
          options,
        ),
      listChanges: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListSkillChanges"].grpc.descriptor,
          request,
          options,
        ),
      reject: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.RejectLearnedSkill"].grpc.descriptor,
          request,
          options,
        ),
      rollback: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.RollbackLearnedSkill"].grpc.descriptor,
          request,
          options,
        ),
    },
    learningAttempts: {
      abandon: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.AbandonLearningAttempt"].grpc.descriptor,
          request,
          options,
        ),
      get: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GetLearningAttempt"].grpc.descriptor,
          request,
          options,
        ),
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListLearningAttempts"].grpc.descriptor,
          request,
          options,
        ),
      retry: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.RetryLearningAttempt"].grpc.descriptor,
          request,
          options,
        ),
    },
    learningProposals: {
      decide: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.DecideLearningProposal"].grpc.descriptor,
          request,
          options,
        ),
      get: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GetLearningProposal"].grpc.descriptor,
          request,
          options,
        ),
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListLearningProposals"].grpc.descriptor,
          request,
          options,
        ),
      undoPromotion: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.UndoLearningPromotion"].grpc.descriptor,
          request,
          options,
        ),
    },
    reflection: {
      reflect: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ReflectSession"].grpc.descriptor,
          request,
          options,
        ),
    },
    schedules: {
      create: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.CreateSchedule"].grpc.descriptor,
          request,
          options,
        ),
      delete: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.DeleteSchedule"].grpc.descriptor,
          request,
          options,
        ),
      fireNow: (request, options) =>
        operations.unary(RPC_CATALOG["ScheduleService.FireNow"].grpc.descriptor, request, options),
      get: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.GetSchedule"].grpc.descriptor,
          request,
          options,
        ),
      getFire: (request, options) =>
        operations.unary(RPC_CATALOG["ScheduleService.GetFire"].grpc.descriptor, request, options),
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.ListSchedules"].grpc.descriptor,
          request,
          options,
        ),
      listFires: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.ListFires"].grpc.descriptor,
          request,
          options,
        ),
      pause: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.PauseSchedule"].grpc.descriptor,
          request,
          options,
        ),
      resume: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.ResumeSchedule"].grpc.descriptor,
          request,
          options,
        ),
      update: (request, options) =>
        operations.unary(
          RPC_CATALOG["ScheduleService.UpdateSchedule"].grpc.descriptor,
          request,
          options,
        ),
    },
    sessionInventory: {
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListSessions"].grpc.descriptor,
          request,
          options,
        ),
    },
    skills: {
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListSkills"].grpc.descriptor,
          request,
          options,
        ),
    },
    soul: {
      get: (request, options) =>
        operations.unary(RPC_CATALOG["HarnessService.GetSoul"].grpc.descriptor, request, options),
    },
    storage: {
      applyCleanup: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ApplySessionCleanup"].grpc.descriptor,
          request,
          options,
        ),
      cancelCleanup: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.CancelSessionCleanup"].grpc.descriptor,
          request,
          options,
        ),
      getCleanupJob: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GetSessionCleanupJob"].grpc.descriptor,
          request,
          options,
        ),
      getHealth: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GetStorageHealth"].grpc.descriptor,
          request,
          options,
        ),
      planCleanup: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.PlanSessionCleanup"].grpc.descriptor,
          request,
          options,
        ),
    },
    userModel: {
      get: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GetUserModel"].grpc.descriptor,
          request,
          options,
        ),
    },
  };
}
