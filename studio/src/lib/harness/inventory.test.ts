import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fetchHarnessCompatibility,
  fetchHarnessUserModel,
  listHarnessAgents,
  listHarnessCommands,
  listHarnessModels,
  listHarnessSkills,
  probeHarness,
  toWireCapabilities,
} from "./inventory";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";

/**
 * Pins the daemon inventory contract over the SDK: the routes the SDK hits
 * (`/v1/commands` keyed by session_id, never a workspace), the UI shapes each
 * list maps to, the never-throw liveness probe, and the compatibility
 * document's SNAKE_CASE capability keys — the vocabulary every UI gate reads
 * (`serverCapabilities.storage_health`, `manual_dream.project_memory`, …).
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("probeHarness", () => {
  it("reports live when the models list answers", async () => {
    const stub = stubHarnessFetch(() => ({ models: [] }));
    await expect(probeHarness()).resolves.toEqual({
      live: true,
      detail: "connected",
    });
    expect(stub.last().url).toBe("/api/mecatl/v1/models");
  });

  it("never throws: a refused daemon folds into {live:false, detail}", async () => {
    stubHarnessFetch(() =>
      jsonResponse(503, { code: "draining", error: "draining" }),
    );
    const status = await probeHarness();
    expect(status.live).toBe(false);
    expect(status.detail).toContain("restarting");
  });

  it("never throws: an unreachable daemon folds into {live:false, detail}", async () => {
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("connection refused");
    });
    const status = await probeHarness();
    expect(status.live).toBe(false);
    expect(status.detail).not.toBe("");
  });
});

describe("fetchHarnessCompatibility", () => {
  it("projects the SDK document onto the wire-keyed capabilities the UI gates read", async () => {
    stubHarnessFetch(() => ({}), {
      api_major: 1,
      features: ["http_steer", "watch_session_events"],
      capabilities: {
        storage_health: true,
        learning_proposals: true,
        reflection: false,
        learned_skills: true,
        manual_compaction: true,
        session_debug: true,
        debug_mcp: false,
        steer: true,
        posture: "trusted",
        manual_dream: {
          project_memory: {
            generate: true,
            decide: false,
            unavailable_reason: "no trusted memory target",
          },
          user_model: { generate: true, decide: true },
        },
      },
    });
    const doc = await fetchHarnessCompatibility();
    expect(doc).not.toBeNull();
    expect(doc?.apiMajor).toBe(1);
    expect(doc?.features).toEqual(["http_steer", "watch_session_events"]);
    expect(doc?.deployment).toBe("");
    expect(doc?.capabilities).toMatchObject({
      storage_health: true,
      learning_proposals: true,
      reflection: false,
      learned_skills: true,
      manual_compaction: true,
      session_debug: true,
      debug_mcp: false,
      steer: true,
      posture: "trusted",
      manual_dream: {
        project_memory: {
          generate: true,
          decide: false,
          unavailable_reason: "no trusted memory target",
        },
        user_model: { generate: true, decide: true, unavailable_reason: "" },
      },
    });
    // No camelCase leaks through: consumers read exactly the wire keys.
    for (const key of Object.keys(doc?.capabilities ?? {})) {
      expect(key).toBe(key.toLowerCase());
    }
  });
});

describe("toWireCapabilities", () => {
  it("is empty for an absent projection", () => {
    expect(toWireCapabilities(undefined)).toEqual({});
  });
});

describe("listHarnessCommands", () => {
  it("GETs /v1/commands keyed by session_id — never a workspace", async () => {
    const stub = stubHarnessFetch(() => ({
      commands: [{ name: "review", description: "Review the diff" }],
    }));
    const commands = await listHarnessCommands("sess-1");
    expect(stub.last()).toMatchObject({
      method: "GET",
      url: "/api/mecatl/v1/commands?session_id=sess-1",
    });
    expect(stub.last().url).not.toContain("workspace");
    expect(commands).toEqual([
      { name: "review", description: "Review the diff" },
    ]);
  });
});

describe("listHarnessModels", () => {
  it("maps int64 context limits to numbers and falls back to the id as display name", async () => {
    const stub = stubHarnessFetch(() => ({
      models: [
        {
          id: "gpt-x",
          provider_id: "openai",
          display_name: "",
          context_limit: 200000,
          image: true,
          reasoning: false,
        },
      ],
    }));
    const models = await listHarnessModels();
    expect(stub.last().url).toBe("/api/mecatl/v1/models");
    expect(models).toEqual([
      {
        id: "gpt-x",
        providerId: "openai",
        displayName: "gpt-x",
        contextLimit: 200000,
        image: true,
        reasoning: false,
      },
    ]);
  });
});

describe("listHarnessAgents / listHarnessSkills", () => {
  it("maps the resolved agent inventory", async () => {
    const stub = stubHarnessFetch(() => ({
      agents: [
        {
          name: "Code Reviewer",
          description: "Reviews diffs",
          model: "",
          tools: ["Read", "Grep"],
          permission_mode: "plan",
          color: "blue",
        },
      ],
    }));
    const agents = await listHarnessAgents();
    expect(stub.last().url).toBe("/api/mecatl/v1/agents");
    expect(agents).toEqual([
      {
        name: "Code Reviewer",
        description: "Reviews diffs",
        model: "",
        tools: ["Read", "Grep"],
        permissionMode: "plan",
        color: "blue",
      },
    ]);
  });

  it("maps the skill inventory with its learned-lifecycle provenance", async () => {
    const stub = stubHarnessFetch(() => ({
      skills: [
        {
          name: "triage",
          description: "Triage flakes",
          agent_owned: true,
          owner_agent: "explorer",
          active_version: "v2",
        },
        { name: "static", description: "External skill" },
      ],
    }));
    const skills = await listHarnessSkills();
    expect(stub.last().url).toBe("/api/mecatl/v1/skills");
    expect(skills).toEqual([
      {
        name: "triage",
        description: "Triage flakes",
        agentOwned: true,
        ownerAgent: "explorer",
        activeVersion: "v2",
      },
      {
        name: "static",
        description: "External skill",
        agentOwned: false,
        ownerAgent: "",
        activeVersion: "",
      },
    ]);
  });
});

describe("fetchHarnessUserModel", () => {
  it("reads the index only (keys + descriptions) and the byte size", async () => {
    const stub = stubHarnessFetch(() => ({
      entries: [{ key: "editor", description: "Prefers vim" }],
      size_bytes: 42,
    }));
    const model = await fetchHarnessUserModel();
    expect(stub.last().url).toBe("/api/mecatl/v1/usermodel");
    expect(model).toEqual({
      entries: [{ key: "editor", description: "Prefers vim" }],
      sizeBytes: 42,
      sha256: "",
    });
  });

  it("surfaces --no-user-model as the typed error (a DISABLED state, not empty data)", async () => {
    stubHarnessFetch(() =>
      jsonResponse(501, {
        code: "unimplemented",
        error: "user model is disabled",
      }),
    );
    await expect(fetchHarnessUserModel()).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "unimplemented",
      status: 501,
    });
  });
});
