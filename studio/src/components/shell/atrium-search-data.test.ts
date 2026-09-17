import { describe, expect, it } from "vitest";
import { SETTINGS_GROUPS } from "@/app/workspace/settings/_components/settings-sections";
import type { AgentSession } from "@/features/agent/types";
import {
  ATRIUM_SEARCH_GROUPS,
  buildAtriumSearchEntries,
  WORKSPACE_PAGE_ENTRIES,
} from "./atrium-search-data";
import { createStaticSearchProvider } from "./search-static";

/**
 * The global search's session surface (the TUI's /sessions filter): a chat
 * matches on its id, model, placement label/branch, kind and every
 * relationship id or name — not only its title — and the inspect-only runs
 * index under their own "Runs" heading, deep-linking to the parent chat
 * with `?inspect=` so the read-only transcript opens.
 */

const session = (partial: Partial<AgentSession> = {}): AgentSession => ({
  id: "main-1",
  title: "Fix the flaky test",
  projectId: null,
  model: "gpt-fixture",
  createdAt: 0,
  updatedAt: 0,
  pinned: false,
  archived: false,
  messageCount: 0,
  isStreaming: false,
  inputTokens: 0,
  outputTokens: 0,
  unread: false,
  estimatedCost: null,
  contextLength: null,
  lastPromptTokens: null,
  thresholdTokens: null,
  kind: "main",
  isChat: true,
  placementLabel: "studio",
  placementBranch: "feat/tabs",
  relationship: {
    parentSessionId: "",
    callId: "",
    branchIndex: null,
    scheduleName: "",
    originSessionId: "origin-9",
    teamId: "",
    memberName: "",
  },
  ...partial,
});

const empty = { jobs: [], skills: [], memories: [] };

describe("buildAtriumSearchEntries — sessions", () => {
  it("indexes a chat on placement label/branch, kind, model, id and relationship terms", () => {
    const [entry] = buildAtriumSearchEntries({
      sessions: [session()],
      ...empty,
    });
    expect(entry.category).toBe("chat");
    expect(entry.href).toBe("/workspace/chat/main-1");
    // The placement label leads the subtitle when known.
    expect(entry.subtitle).toBe("studio");
    expect(entry.keywords).toEqual([
      "main-1",
      "gpt-fixture",
      "studio",
      "feat/tabs",
      "main",
      "origin-9",
    ]);

    const provider = createStaticSearchProvider([entry]);
    for (const query of ["feat/tabs", "studio", "origin-9", "gpt-fixture"]) {
      expect(provider.query(query).map((r) => r.entry.id)).toEqual([
        "chat-main-1",
      ]);
    }
    expect(provider.query("nightly")).toEqual([]);
  });

  it("falls back to model then a plain label when the chat has no placement", () => {
    const [entry] = buildAtriumSearchEntries({
      sessions: [session({ placementLabel: "", placementBranch: "" })],
      ...empty,
    });
    expect(entry.subtitle).toBe("gpt-fixture");
    expect(entry.keywords).not.toContain("");
  });

  it("emits run entries under the Runs heading with the inspect deep link", () => {
    const entries = buildAtriumSearchEntries({
      sessions: [],
      runs: [
        session({
          id: "subagent-1",
          title: "",
          kind: "subagent",
          isChat: false,
          relationship: {
            parentSessionId: "main-1",
            callId: "call-7",
            branchIndex: null,
            scheduleName: "",
            originSessionId: "",
            teamId: "",
            memberName: "",
          },
        }),
        session({
          id: "fire-1",
          title: "Nightly digest",
          kind: "scheduled",
          isChat: false,
          relationship: {
            parentSessionId: "",
            callId: "",
            branchIndex: null,
            scheduleName: "nightly",
            originSessionId: "",
            teamId: "",
            memberName: "",
          },
        }),
      ],
      ...empty,
    });
    expect(entries.map((e) => e.category)).toEqual(["run", "run"]);
    expect(entries[0]).toMatchObject({
      id: "run-subagent-1",
      title: "Subagent of main-1 · call call-7",
      subtitle: "Subagent",
      href: "/workspace/chat/main-1?inspect=subagent-1",
    });
    expect(entries[0].keywords).toEqual(
      expect.arrayContaining(["subagent-1", "main-1", "call-7", "subagent"]),
    );
    // A titled run keeps its title and names the relationship in the subtitle;
    // a run without a parent opens on the draft route.
    expect(entries[1]).toMatchObject({
      title: "Nightly digest",
      subtitle: "Scheduled run · Fire of schedule nightly",
      href: "/workspace/chat?inspect=fire-1",
    });
    const provider = createStaticSearchProvider(entries);
    expect(provider.query("call-7").map((r) => r.entry.id)).toEqual([
      "run-subagent-1",
    ]);
    expect(provider.query("nightly").map((r) => r.entry.id)).toEqual([
      "run-fire-1",
    ]);
  });

  it("lists a Runs group right after Chats", () => {
    expect(ATRIUM_SEARCH_GROUPS.slice(0, 2)).toEqual([
      { category: "chat", heading: "Chats", source: "atrium" },
      { category: "run", heading: "Runs", source: "atrium" },
    ]);
  });
});

/**
 * The static help pages: ⌘K is the help entry point mecatui's `help` command
 * is, so Help & about and the keyboard shortcuts reference index under a
 * Pages group and match on "help" — and the Help href is the same one the
 * settings IA links, so the two cannot drift apart.
 */
describe("WORKSPACE_PAGE_ENTRIES", () => {
  it("indexes Help & about and Keyboard shortcuts under the Pages group", () => {
    expect(WORKSPACE_PAGE_ENTRIES.map((e) => e.category)).toEqual([
      "page",
      "page",
    ]);
    expect(WORKSPACE_PAGE_ENTRIES).toEqual([
      expect.objectContaining({
        title: "Help & about",
        href: "/workspace/settings/help",
      }),
      expect.objectContaining({
        title: "Keyboard shortcuts",
        href: "/workspace/shortcuts",
      }),
    ]);
    expect(ATRIUM_SEARCH_GROUPS).toContainEqual({
      category: "page",
      heading: "Pages",
      source: "atrium",
    });
  });

  it("matches both on 'help' and the Help page on 'version' and 'docs'", () => {
    const provider = createStaticSearchProvider(WORKSPACE_PAGE_ENTRIES);
    expect(provider.query("help").map((r) => r.entry.title)).toEqual([
      "Help & about",
      "Keyboard shortcuts",
    ]);
    for (const query of ["version", "docs", "configuration"]) {
      expect(provider.query(query).map((r) => r.entry.title)).toEqual([
        "Help & about",
      ]);
    }
  });

  it("links the Help page the settings IA lists", () => {
    const settingsHrefs = SETTINGS_GROUPS.flatMap((group) =>
      group.items.map((item) => item.href),
    );
    const help = WORKSPACE_PAGE_ENTRIES.find((e) => e.title === "Help & about");
    expect(help).toBeDefined();
    expect(settingsHrefs).toContain(help?.href);
  });
});
