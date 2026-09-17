import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import {
  availabilityLabel,
  COMPOSER_INSERT_LIMIT,
  catalogueLabel,
  clampComposerInsert,
  connectorToolCount,
  enrollmentLabel,
  getHarnessMcpPrompt,
  isNoMcpProvider,
  listHarnessMcpPrompts,
  listHarnessMcpResources,
  listHarnessMcpSources,
  listHarnessToolHiveGroups,
  readHarnessMcpResource,
  renderPromptForComposer,
} from "./mcp";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";

/**
 * Pins the MCP inventory adapter behind the `/mcp` panel's Studio analogue:
 * the source read hits exactly GET /v1/mcp/sources and projects every
 * source with its servers and skip reasons; the ToolHive groups read hits
 * exactly GET /v1/mcp/toolhive/groups; the daemon's `no_mcp_provider`
 * refusal is classified; and the broker labels are the TUI's words.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("listHarnessMcpSources", () => {
  it("reads GET /v1/mcp/sources and projects sources, servers and diagnostics", async () => {
    const { requests } = stubHarnessFetch((request) =>
      request.path === "/v1/mcp/sources"
        ? jsonResponse(200, {
            sources: [
              {
                name: "static",
                kind: "static",
                enabled: true,
                servers: [
                  {
                    name: "github",
                    url: "http://127.0.0.1:1/gh",
                    transport: "streamable-http",
                  },
                ],
              },
              {
                name: "toolhive(default)",
                kind: "toolhive",
                enabled: false,
                group: "default",
                servers: [],
                diagnostics: ["fetch skipped: unsupported transport stdio", ""],
              },
            ],
          })
        : undefined,
    );
    await expect(listHarnessMcpSources()).resolves.toEqual([
      {
        name: "static",
        kind: "static",
        enabled: true,
        group: "",
        servers: [
          {
            name: "github",
            url: "http://127.0.0.1:1/gh",
            transport: "streamable-http",
            group: "",
          },
        ],
        diagnostics: [],
      },
      {
        name: "toolhive(default)",
        kind: "toolhive",
        enabled: false,
        group: "default",
        servers: [],
        diagnostics: ["fetch skipped: unsupported transport stdio"],
      },
    ]);
    expect(requests).toHaveLength(1);
    expect(requests[0].method).toBe("GET");
    expect(requests[0].url).toBe("/api/mecatl/v1/mcp/sources");
  });

  it("returns an empty list when the daemon resolved no source", async () => {
    stubHarnessFetch((request) =>
      request.path === "/v1/mcp/sources" ? jsonResponse(200, {}) : undefined,
    );
    await expect(listHarnessMcpSources()).resolves.toEqual([]);
  });

  it("throws the typed error, classified as no_mcp_provider, on the daemon's 412", async () => {
    stubHarnessFetch(() =>
      problemResponse(412, "no_mcp_provider", "No MCP provider configured"),
    );
    const failure = await listHarnessMcpSources().catch((error) => error);
    expect(failure).toBeInstanceOf(HarnessApiError);
    expect(isNoMcpProvider(failure)).toBe(true);
    expect(isNoMcpProvider(new Error("other"))).toBe(false);
  });
});

describe("listHarnessToolHiveGroups", () => {
  it("reads GET /v1/mcp/toolhive/groups and drops blank groups", async () => {
    const { requests } = stubHarnessFetch((request) =>
      request.path === "/v1/mcp/toolhive/groups"
        ? jsonResponse(200, { groups: ["default", "", "research"] })
        : undefined,
    );
    await expect(listHarnessToolHiveGroups()).resolves.toEqual([
      "default",
      "research",
    ]);
    expect(requests[0].method).toBe("GET");
    expect(requests[0].url).toBe("/api/mecatl/v1/mcp/toolhive/groups");
  });

  it("rethrows the typed error so the caller can degrade to 'unavailable'", async () => {
    stubHarnessFetch(() => problemResponse(404, "not_found", "no such route"));
    await expect(listHarnessToolHiveGroups()).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 404,
    });
  });
});

describe("broker labels", () => {
  it("words the enrollment state as the TUI does", () => {
    expect(enrollmentLabel("not_required")).toBe("No setup required");
    expect(enrollmentLabel("not_started")).toBe("No active setup");
    expect(enrollmentLabel("pending")).toBe("Setup in progress");
    expect(enrollmentLabel("completed")).toBe("Catalogue ready");
    expect(enrollmentLabel("unknown")).toBe("Status unavailable");
    expect(enrollmentLabel("")).toBe("Status unavailable");
  });

  it("words the catalogue state as the TUI does", () => {
    expect(catalogueLabel("hidden")).toBe("Awaiting discovery");
    expect(catalogueLabel("declared")).toBe("Tools declared");
    expect(catalogueLabel("discovered")).toBe("Tools discovered");
    expect(catalogueLabel("bogus")).toBe("Status unavailable");
  });

  it("shows a tool count only for a declared or discovered catalogue", () => {
    expect(
      connectorToolCount({ catalogueState: "discovered", toolCount: 12 }),
    ).toBe("12");
    expect(
      connectorToolCount({ catalogueState: "declared", toolCount: 0 }),
    ).toBe("0");
    expect(connectorToolCount({ catalogueState: "hidden", toolCount: 7 })).toBe(
      "—",
    );
  });

  it("distinguishes a broker-reported outage from an unknown availability", () => {
    expect(availabilityLabel("unavailable")).toBe("Broker state unavailable");
    expect(availabilityLabel("weird")).toBe("Status unavailable");
  });
});

/**
 * The composer pickers' reads (the TUI's f8 prompts and ctrl+r resources):
 * exact routes and query/body shapes, every field normalised, text chunks
 * joined while blobs are reported rather than inserted, the TUI's
 * role-prefixed prompt join, and the 64 KiB insertion clamp.
 */
describe("listHarnessMcpPrompts", () => {
  it("reads GET /v1/mcp/prompts (an empty server scope is omitted) and projects prompts with their arguments", async () => {
    const { requests } = stubHarnessFetch((request) =>
      request.path === "/v1/mcp/prompts"
        ? jsonResponse(200, {
            prompts: [
              {
                server: "github",
                name: "summarize",
                title: "Summarize",
                description: "Summarizes a file.",
                arguments: [
                  { name: "path", title: "Path", required: true },
                  { name: "tone", description: "Optional tone." },
                ],
              },
              { server: "slack", name: "bare" },
            ],
          })
        : undefined,
    );
    await expect(listHarnessMcpPrompts()).resolves.toEqual([
      {
        server: "github",
        name: "summarize",
        title: "Summarize",
        description: "Summarizes a file.",
        arguments: [
          { name: "path", title: "Path", description: "", required: true },
          {
            name: "tone",
            title: "",
            description: "Optional tone.",
            required: false,
          },
        ],
      },
      {
        server: "slack",
        name: "bare",
        title: "",
        description: "",
        arguments: [],
      },
    ]);
    expect(requests.map((r) => `${r.method} ${r.path}`)).toEqual([
      "GET /v1/mcp/prompts",
    ]);
  });

  it("scopes the listing to one server through the query", async () => {
    stubHarnessFetch((request) =>
      request.path === "/v1/mcp/prompts?server=github"
        ? jsonResponse(200, { prompts: [] })
        : undefined,
    );
    await expect(listHarnessMcpPrompts("github")).resolves.toEqual([]);
  });

  it("surfaces the daemon's no_mcp_provider refusal as the classified error", async () => {
    stubHarnessFetch(() =>
      problemResponse(412, "no_mcp_provider", "no MCP provider is wired"),
    );
    const error = await listHarnessMcpPrompts().catch((e) => e);
    expect(error).toBeInstanceOf(HarnessApiError);
    expect(isNoMcpProvider(error)).toBe(true);
  });
});

describe("getHarnessMcpPrompt", () => {
  it("posts {server,name,arguments} to /v1/mcp/prompts/get and projects the rendered messages", async () => {
    const { last } = stubHarnessFetch((request) =>
      request.path === "/v1/mcp/prompts/get" && request.method === "POST"
        ? jsonResponse(200, {
            description: "Summarizes a file.",
            messages: [
              { role: "user", text: "Summarize README.md" },
              { role: "assistant", text: "Reading it." },
            ],
          })
        : undefined,
    );
    await expect(
      getHarnessMcpPrompt("github", "summarize", { path: "README.md" }),
    ).resolves.toEqual({
      description: "Summarizes a file.",
      messages: [
        { role: "user", text: "Summarize README.md" },
        { role: "assistant", text: "Reading it." },
      ],
    });
    expect(last().body).toEqual({
      server: "github",
      name: "summarize",
      arguments: { path: "README.md" },
    });
  });
});

describe("listHarnessMcpResources", () => {
  it("reads GET /v1/mcp/resources (an empty server scope is omitted) and projects every field, size as a number", async () => {
    const { requests } = stubHarnessFetch((request) =>
      request.path === "/v1/mcp/resources"
        ? jsonResponse(200, {
            resources: [
              {
                server: "github",
                uri: "file:///notes.txt",
                name: "notes.txt",
                title: "Notes",
                description: "Release notes.",
                mime_type: "text/plain",
                size: "2048",
                read_only: true,
              },
              { server: "slack", uri: "slack://channel/general" },
            ],
          })
        : undefined,
    );
    await expect(listHarnessMcpResources()).resolves.toEqual([
      {
        server: "github",
        uri: "file:///notes.txt",
        name: "notes.txt",
        title: "Notes",
        description: "Release notes.",
        mimeType: "text/plain",
        size: 2048,
        readOnly: true,
      },
      {
        server: "slack",
        uri: "slack://channel/general",
        name: "",
        title: "",
        description: "",
        mimeType: "",
        size: 0,
        readOnly: false,
      },
    ]);
    expect(requests.map((r) => `${r.method} ${r.path}`)).toEqual([
      "GET /v1/mcp/resources",
    ]);
  });
});

describe("readHarnessMcpResource", () => {
  it("reads GET /v1/mcp/resources/read?server=&uri=, joins text chunks and reports blobs without inserting them", async () => {
    const { requests } = stubHarnessFetch((request) =>
      request.path.startsWith("/v1/mcp/resources/read?")
        ? jsonResponse(200, {
            contents: [
              {
                uri: "file:///notes.txt",
                mime_type: "text/plain",
                text: "one",
              },
              {
                uri: "file:///notes.txt",
                mime_type: "image/png",
                // Four bytes, base64 — the proto JSON form of `bytes`.
                blob: "iVBORw==",
              },
              {
                uri: "file:///notes.txt",
                mime_type: "text/plain",
                text: "two",
              },
            ],
          })
        : undefined,
    );
    await expect(
      readHarnessMcpResource("github", "file:///notes.txt"),
    ).resolves.toEqual({
      text: "one\ntwo",
      binaryChunks: [{ mimeType: "image/png", bytes: 4 }],
    });
    const url = new URL(requests[0]?.url ?? "", "http://studio");
    expect(url.pathname).toBe("/api/mecatl/v1/mcp/resources/read");
    expect(url.searchParams.get("server")).toBe("github");
    expect(url.searchParams.get("uri")).toBe("file:///notes.txt");
  });
});

describe("renderPromptForComposer", () => {
  it("prefixes each message with its role and separates messages with a blank line (the TUI's joinPromptMessages)", () => {
    expect(
      renderPromptForComposer([
        { role: "user", text: "Summarize README.md" },
        { role: "assistant", text: "Reading it." },
      ]),
    ).toBe("user: Summarize README.md\n\nassistant: Reading it.");
  });

  it("leaves a role-less message bare and an empty list empty", () => {
    expect(renderPromptForComposer([{ role: "", text: "plain" }])).toBe(
      "plain",
    );
    expect(renderPromptForComposer([])).toBe("");
  });
});

describe("clampComposerInsert", () => {
  it("passes text within the 64 KiB limit through untouched", () => {
    const text = "x".repeat(COMPOSER_INSERT_LIMIT);
    expect(COMPOSER_INSERT_LIMIT).toBe(65_536);
    expect(clampComposerInsert(text)).toBe(text);
  });

  it("cuts longer text at the limit and says how much was dropped", () => {
    const clamped = clampComposerInsert("y".repeat(COMPOSER_INSERT_LIMIT + 3));
    expect(clamped.startsWith("y".repeat(COMPOSER_INSERT_LIMIT))).toBe(true);
    expect(
      clamped.endsWith("\n[truncated: 3 more characters not inserted]"),
    ).toBe(true);
    expect(clampComposerInsert("abcdef", 4)).toBe(
      "abcd\n[truncated: 2 more characters not inserted]",
    );
  });
});
