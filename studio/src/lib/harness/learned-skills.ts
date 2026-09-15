/**
 * Learned-skill lifecycle (ADR 0110) — the human half of the daemon's
 * self-improvement loop, over the SDK's `client.learnedSkills` namespace.
 *
 * Wire: `GET /v1/skills/learned` (+ `/changes`, `/{id}`, `/{id}/diff`) and
 * `POST /v1/skills/learned/{id}/{activate|reject|archive|rollback}`. Gated by
 * `capabilities.learned_skills` on GET /v1/compatibility.
 *
 * Every mutation carries the version being acted on plus `expected_revision`
 * — the optimistic-concurrency token off the listed row. The `project` field
 * is deliberately never set: Studio reads the operator-scope partition (the
 * browser never knows the workspace path).
 */

import type {
  MutateLearnedSkillResponse,
  LearnedSkillVersion as ProtoLearnedSkillVersion,
  SkillChangeReceipt,
} from "@stacklok-oss/mecatl-sdk/gen";

import { getHarnessClient, harness } from "./sdk";
import { timestampUnix } from "./time";

/**
 * One immutable agent-owned skill version. `state` is the daemon's closed
 * vocabulary: draft, evaluated, staged, active, archived, rejected.
 */
export interface LearnedSkillVersion {
  id: string;
  name: string;
  version: string;
  /** Optimistic-concurrency token — every mutation must carry it. */
  revision: string;
  state: string;
  ownerAgent: string;
  description: string;
  /** Bounded, server-repaired body preview. */
  body: string;
  /** The prior version this one replaced ("" for a first version). */
  supersedes: string;
  evidenceCount: number;
  createdAtUnix: number;
  updatedAtUnix: number;
  inspectAvailable: boolean;
  undoAvailable: boolean;
}

function decodeLearnedSkillVersion(
  skill: ProtoLearnedSkillVersion | undefined,
): LearnedSkillVersion {
  return {
    id: skill?.id ?? "",
    name: skill?.name ?? "",
    version: skill?.version ?? "",
    revision: skill?.revision ?? "",
    state: skill?.state ?? "",
    ownerAgent: skill?.ownerAgent ?? "",
    description: skill?.description ?? "",
    body: skill?.body ?? "",
    supersedes: skill?.supersedes ?? "",
    evidenceCount: skill?.evidenceCount ?? 0,
    createdAtUnix: timestampUnix(skill?.createdAt),
    updatedAtUnix: timestampUnix(skill?.updatedAt),
    inspectAvailable: skill?.inspectAvailable ?? false,
    undoAvailable: skill?.undoAvailable ?? false,
  };
}

/** One append-only lifecycle receipt from `GET /v1/skills/learned/changes`. */
export interface LearnedSkillChange {
  id: string;
  skillId: string;
  name: string;
  version: string;
  operation: string;
  fromState: string;
  toState: string;
  evidenceCount: number;
  verdict: string;
  atUnix: number;
}

function decodeLearnedSkillChange(
  change: SkillChangeReceipt,
): LearnedSkillChange {
  return {
    id: change.id,
    skillId: change.skillId,
    name: change.name,
    version: change.version,
    operation: change.operation,
    fromState: change.fromState,
    toState: change.toState,
    evidenceCount: change.evidenceCount,
    verdict: change.verdict,
    atUnix: timestampUnix(change.at),
  };
}

export interface LearnedSkillPage {
  skills: LearnedSkillVersion[];
  nextCursor: string;
}

export async function listLearnedSkills(
  options: { state?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<LearnedSkillPage> {
  const response = await harness(() =>
    getHarnessClient().learnedSkills.list(
      {
        $typeName: "mecatl.v1.ListLearnedSkillsRequest",
        state: options.state ?? "",
        cursor: options.cursor ?? "",
        limit: options.limit ?? 0,
        ownerAgent: "",
        project: "", // operator scope — never the workspace (rule 2)
      },
      { signal },
    ),
  );
  return {
    skills: response.skills.map(decodeLearnedSkillVersion),
    nextCursor: response.nextCursor,
  };
}

export async function listLearnedSkillChanges(
  options: { cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<{ changes: LearnedSkillChange[]; nextCursor: string }> {
  const response = await harness(() =>
    getHarnessClient().learnedSkills.listChanges(
      {
        $typeName: "mecatl.v1.ListSkillChangesRequest",
        cursor: options.cursor ?? "",
        limit: options.limit ?? 0,
        project: "",
      },
      { signal },
    ),
  );
  return {
    changes: response.changes.map(decodeLearnedSkillChange),
    nextCursor: response.nextCursor,
  };
}

/** Reads one version in full (the list body is a bounded preview). */
export async function fetchLearnedSkill(
  id: string,
  ownerAgent: string,
  version?: string,
  signal?: AbortSignal,
): Promise<LearnedSkillVersion> {
  const response = await harness(() =>
    getHarnessClient().learnedSkills.get(
      {
        $typeName: "mecatl.v1.GetLearnedSkillRequest",
        id,
        ownerAgent,
        version: version ?? "",
        project: "",
      },
      { signal },
    ),
  );
  return decodeLearnedSkillVersion(response.skill);
}

/** Unified diff between two versions of one learned skill. */
export async function diffLearnedSkillVersions(
  id: string,
  ownerAgent: string,
  fromVersion: string,
  toVersion: string,
  signal?: AbortSignal,
): Promise<string> {
  const response = await harness(() =>
    getHarnessClient().learnedSkills.diffVersions(
      {
        $typeName: "mecatl.v1.DiffLearnedSkillVersionsRequest",
        id,
        ownerAgent,
        fromVersion,
        toVersion,
        project: "",
      },
      { signal },
    ),
  );
  return response.diff;
}

export type LearnedSkillAction = "activate" | "reject" | "archive";

export interface LearnedSkillMutationResult {
  skill: LearnedSkillVersion;
  /** How republishing to the live skill snapshot went, when it applies. */
  publicationStatus: string;
  publicationError: string;
}

function decodeMutation(
  response: MutateLearnedSkillResponse,
): LearnedSkillMutationResult {
  return {
    skill: decodeLearnedSkillVersion(response.skill),
    publicationStatus: response.publicationStatus,
    publicationError: response.publicationError,
  };
}

/** Activate / reject / archive one version, guarded by its revision. */
export async function mutateLearnedSkill(
  action: LearnedSkillAction,
  target: {
    id: string;
    ownerAgent: string;
    version: string;
    expectedRevision: string;
  },
): Promise<LearnedSkillMutationResult> {
  const request = {
    $typeName: "mecatl.v1.MutateLearnedSkillRequest" as const,
    project: "",
    id: target.id,
    ownerAgent: target.ownerAgent,
    version: target.version,
    expectedRevision: target.expectedRevision,
  };
  const skills = getHarnessClient().learnedSkills;
  const response = await harness(() => skills[action](request));
  return decodeMutation(response);
}

/** Rolls the skill back to a prior version (usually `supersedes`). */
export async function rollbackLearnedSkill(target: {
  id: string;
  ownerAgent: string;
  targetVersion: string;
  expectedRevision: string;
}): Promise<LearnedSkillMutationResult> {
  const response = await harness(() =>
    getHarnessClient().learnedSkills.rollback({
      $typeName: "mecatl.v1.RollbackLearnedSkillRequest",
      project: "",
      id: target.id,
      ownerAgent: target.ownerAgent,
      targetVersion: target.targetVersion,
      expectedRevision: target.expectedRevision,
    }),
  );
  return decodeMutation(response);
}
