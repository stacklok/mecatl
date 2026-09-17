import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fetchHarnessCompatibility,
  fetchHarnessUserModel,
  fetchHarnessUserModelEntry,
  listHarnessAgents,
  listHarnessCommands,
  listHarnessModelInventory,
  listHarnessModels,
  listHarnessSkills,
  probeHarness,
  toWireCapabilities,
} from "./inventory";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";

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
      status: 200,
      code: "",
    });
    expect(stub.last().url).toBe("/api/mecatl/v1/models");
  });

  it("never throws: a refused daemon folds into {live:false, detail, status, code}", async () => {
    stubHarnessFetch(() =>
      jsonResponse(503, { code: "draining", error: "draining" }),
    );
    const status = await probeHarness();
    expect(status.live).toBe(false);
    expect(status.detail).toContain("restarting");
    expect(status.status).toBe(503);
    expect(status.code).toBe("draining");
  });

  it("types a refused credential so the offline banner can name it", async () => {
    stubHarnessFetch(() =>
      jsonResponse(401, {
        code: "unauthenticated",
        error: "missing or invalid bearer token",
      }),
    );
    await expect(probeHarness()).resolves.toEqual({
      live: false,
      detail: "missing or invalid bearer token",
      status: 401,
      code: "unauthenticated",
    });
  });

  it("never throws: an unreachable daemon folds into {live:false, detail}", async () => {
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("connection refused");
    });
    const status = await probeHarness();
    expect(status.live).toBe(false);
    expect(status.detail).not.toBe("");
  });

  it("keeps the proxy's oidc_session_expired code through the SDK's 401 collapse, so the sign-in banner can route it", async () => {
    // The SDK folds every 401 into AuthenticationError("Authentication
    // failed") before reading the body; the proxy's code survives only on
    // the error's `cause`, which toHarnessError reads back.
    stubHarnessFetch(() =>
      problemResponse(
        401,
        "oidc_session_expired",
        "The OIDC session expired — sign in again from Settings.",
      ),
    );
    await expect(probeHarness()).resolves.toEqual({
      live: false,
      detail: "The OIDC session expired — sign in again from Settings.",
      status: 401,
      code: "oidc_session_expired",
    });
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

describe("listHarnessModelInventory", () => {
  it("maps provider_status rows (state passthrough, count coerced, flags defaulted)", async () => {
    stubHarnessFetch(() => ({
      models: [],
      provider_status: [
        {
          provider_id: "toolhive",
          state: "unreachable",
          hint: "start it with `thv llm proxy start`",
          model_count: 0,
        },
        {
          provider_id: "openai-codex",
          state: "ok",
          hint: "",
          default_model_auto_selected: true,
          model_count: 3,
          available_not_default: true,
        },
      ],
    }));
    const inventory = await listHarnessModelInventory();
    expect(inventory.models).toEqual([]);
    expect(inventory.providerStatus).toEqual([
      {
        providerId: "toolhive",
        state: "unreachable",
        hint: "start it with `thv llm proxy start`",
        defaultModelAutoSelected: false,
        modelCount: 0,
        availableNotDefault: false,
      },
      {
        providerId: "openai-codex",
        state: "ok",
        hint: "",
        defaultModelAutoSelected: true,
        modelCount: 3,
        availableNotDefault: true,
      },
    ]);
  });

  it("reads an older daemon's list (no provider_status) as an empty status set", async () => {
    stubHarnessFetch(() => ({
      models: [{ id: "m", provider_id: "openrouter" }],
    }));
    const inventory = await listHarnessModelInventory();
    expect(inventory.providerStatus).toEqual([]);
    expect(inventory.models.map((model) => model.id)).toEqual(["m"]);
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
  it("reads the index (keys + descriptions), the byte size and the digest", async () => {
    const stub = stubHarnessFetch(() => ({
      entries: [{ key: "editor", description: "Prefers vim" }],
      size_bytes: 42,
      sha256: "a".repeat(64),
    }));
    const model = await fetchHarnessUserModel();
    // The index read is key-less: no `?key=` reaches the daemon.
    expect(stub.last().url).toBe("/api/mecatl/v1/usermodel");
    expect(model).toEqual({
      entries: [{ key: "editor", description: "Prefers vim" }],
      sizeBytes: 42,
      sha256: "a".repeat(64),
    });
  });

  it('folds a daemon that sends no digest into sha256 ""', async () => {
    stubHarnessFetch(() => ({ entries: [], size_bytes: 0 }));
    await expect(fetchHarnessUserModel()).resolves.toMatchObject({
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

describe("fetchHarnessUserModelEntry", () => {
  const wireRevision = {
    key: "editor config",
    value: "Tabs, width 4",
    description: "Prefers tabs",
    version: "3",
    status: "active",
    writer: "agent",
    origin: "reflection",
    source_session_id: "s-1",
    source_proposal_id: "p-9",
    updated_at: { seconds: 1_755_000_000 },
  };

  it("sends the key as `?key=` and decodes current, history and history_available", async () => {
    const stub = stubHarnessFetch(() => ({
      entries: [{ key: "editor config", description: "Prefers tabs" }],
      size_bytes: 42,
      sha256: "b".repeat(64),
      detail: {
        current: wireRevision,
        history: [
          {
            version: "2",
            status: "superseded",
            updated_at: { seconds: 1_754_000_000 },
          },
        ],
        history_available: true,
      },
    }));
    const detail = await fetchHarnessUserModelEntry("editor config");
    // URLSearchParams encoding: the space becomes `+`, the key rides the query.
    expect(stub.last().url).toBe("/api/mecatl/v1/usermodel?key=editor+config");
    expect(detail).toEqual({
      current: {
        key: "editor config",
        value: "Tabs, width 4",
        description: "Prefers tabs",
        version: "3",
        status: "active",
        writer: "agent",
        origin: "reflection",
        sourceSessionId: "s-1",
        sourceProposalId: "p-9",
        updatedAtUnix: 1_755_000_000,
      },
      history: [
        {
          key: "",
          value: "",
          description: "",
          version: "2",
          status: "superseded",
          writer: "",
          origin: "",
          sourceSessionId: "",
          sourceProposalId: "",
          updatedAtUnix: 1_754_000_000,
        },
      ],
      historyAvailable: true,
    });
  });

  it("reports history_available=false honestly (legacy store) with an unset timestamp as 0", async () => {
    stubHarnessFetch(() => ({
      entries: [],
      detail: {
        current: { ...wireRevision, updated_at: undefined },
        history: [],
        history_available: false,
      },
    }));
    const detail = await fetchHarnessUserModelEntry("editor config");
    expect(detail?.historyAvailable).toBe(false);
    expect(detail?.history).toEqual([]);
    expect(detail?.current.updatedAtUnix).toBe(0);
  });

  it("resolves null when the daemon answers without `detail` (the key no longer matches)", async () => {
    stubHarnessFetch(() => ({
      entries: [{ key: "other", description: "x" }],
      size_bytes: 1,
    }));
    await expect(fetchHarnessUserModelEntry("gone")).resolves.toBeNull();
  });

  it("surfaces --no-user-model as the same typed error the index read throws", async () => {
    stubHarnessFetch(() =>
      jsonResponse(501, {
        code: "unimplemented",
        error: "user model is disabled",
      }),
    );
    await expect(fetchHarnessUserModelEntry("editor")).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "unimplemented",
      status: 501,
    });
  });
});
