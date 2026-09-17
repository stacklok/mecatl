import { createHash } from "node:crypto";
import {
  mkdirSync,
  mkdtempSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import {
  ANCHOR_DIRS,
  hasProjectAuthority,
  jsonHasPermissionsAllow,
  PROJECT_SOUL_REL,
  TRUST_DECISIONS,
  trustAnchor,
  trustDecision,
  yamlHasPermissionsAllow,
} from "./controller-trust.mjs";

/**
 * The controller's OWN project-trust registry probes, over real
 * directories: which workspaces carry an authority set a grant would admit
 * (hasProjectAuthority mirrors internal/adapter/workspacetrust/authority.go),
 * the identity anchor whose drift withholds a remembered grant (trustAnchor
 * mirrors anchor.go byte-for-byte — the transcript grammar is pinned here
 * with an independently computed digest), and the decision `/status.trust`
 * reports for a spawn. Every probe fails SAFE: an unresolvable workspace,
 * a symlink where a regular file is expected, or unreadable YAML/JSON reads
 * "no authority" / "absent", never as a grant.
 *
 * Fixtures live under the repo-local, git-ignored `studio/.scratch/` (the
 * house rule for scratch files) and are removed after each test.
 */

const fixturesRoot = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../../.scratch/vitest-controller-trust",
);
const created: string[] = [];

beforeAll(() => {
  mkdirSync(fixturesRoot, { recursive: true });
});

afterEach(() => {
  for (const dir of created.splice(0)) {
    rmSync(dir, { recursive: true, force: true });
  }
});

/** A fresh empty workspace under the fixtures root. */
function workspace(): string {
  const dir = mkdtempSync(join(fixturesRoot, "ws-"));
  created.push(dir);
  return dir;
}

/** Writes `rel` (slash path) under `root`, creating parents. */
function file(root: string, rel: string, content: string | Buffer = "x") {
  const path = join(root, ...rel.split("/"));
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content);
  return path;
}

const sha256 = (data: string | Buffer) =>
  createHash("sha256").update(data).digest("hex");

/**
 * anchor.go's transcript, recomputed independently of the implementation:
 * `<rel>\0<sha256hex(bytes)>\n` per member (or `<rel>\0absent:<rel>\n`),
 * members sorted bytewise by root-relative slash path.
 */
function expectedAnchor(
  members: { rel: string; bytes: string | Buffer | null }[],
): string {
  const sorted = [...members].sort((a, b) =>
    Buffer.compare(Buffer.from(a.rel, "utf8"), Buffer.from(b.rel, "utf8")),
  );
  const transcript = createHash("sha256");
  for (const member of sorted) {
    transcript.update(member.rel, "utf8");
    transcript.update(Buffer.from([0]));
    transcript.update(
      member.bytes === null ? `absent:${member.rel}` : sha256(member.bytes),
      "utf8",
    );
    transcript.update("\n", "utf8");
  }
  return transcript.digest("hex");
}

const SOUL_ABSENT = { rel: PROJECT_SOUL_REL, bytes: null };

describe("the project-tier layout", () => {
  it("scans the six anchor dirs the daemon folds, bytewise-sorted, and the project soul", () => {
    expect([...ANCHOR_DIRS]).toEqual([
      ".claude/agents",
      ".claude/commands",
      ".claude/skills",
      ".mecatl/agents",
      ".mecatl/commands",
      ".mecatl/skills",
    ]);
    expect(Object.isFrozen(ANCHOR_DIRS)).toBe(true);
    expect(PROJECT_SOUL_REL).toBe(".mecatl/soul.md");
  });
});

describe("hasProjectAuthority", () => {
  it("is false for an empty workspace and fail-safe false for anything that does not resolve", () => {
    expect(hasProjectAuthority(workspace())).toBe(false);
    expect(hasProjectAuthority(join(fixturesRoot, "does-not-exist"))).toBe(
      false,
    );
    expect(hasProjectAuthority("")).toBe(false);
    // Not a string at all (a controller bug, not a workspace): still false.
    expect(hasProjectAuthority(undefined as unknown as string)).toBe(false);
    // A regular file is not a workspace.
    const ws = workspace();
    expect(hasProjectAuthority(file(ws, "README.md"))).toBe(false);
  });

  it("is true for a project soul that is a regular file — not a directory or a symlink", () => {
    const withSoul = workspace();
    file(withSoul, PROJECT_SOUL_REL, "# soul");
    expect(hasProjectAuthority(withSoul)).toBe(true);

    const soulDir = workspace();
    mkdirSync(join(soulDir, ".mecatl", "soul.md"), { recursive: true });
    expect(hasProjectAuthority(soulDir)).toBe(false);

    const soulLink = workspace();
    const target = file(soulLink, "elsewhere/soul.md", "# soul");
    mkdirSync(join(soulLink, ".mecatl"), { recursive: true });
    symlinkSync(target, join(soulLink, ".mecatl", "soul.md"));
    expect(hasProjectAuthority(soulLink)).toBe(false);
  });

  it("is true for any regular file nested under an anchor dir", () => {
    for (const dir of ANCHOR_DIRS) {
      const ws = workspace();
      file(ws, `${dir}/nested/deeper/DEF.md`, "body");
      expect(hasProjectAuthority(ws), dir).toBe(true);
    }
  });

  it("ignores empty anchor dirs, empty subdirs and symlinks under them", () => {
    const ws = workspace();
    mkdirSync(join(ws, ".claude", "skills", "empty"), { recursive: true });
    expect(hasProjectAuthority(ws)).toBe(false);

    // A symlink to a regular file elsewhere is not a member (Lstat).
    const target = file(ws, "outside/SKILL.md", "body");
    symlinkSync(target, join(ws, ".claude", "skills", "link.md"));
    expect(hasProjectAuthority(ws)).toBe(false);

    // Nor is a symlinked directory traversed.
    file(ws, "outside/dir/agent.md", "body");
    symlinkSync(
      join(ws, "outside", "dir"),
      join(ws, ".claude", "skills", "linked-dir"),
    );
    expect(hasProjectAuthority(ws)).toBe(false);
  });

  it("is true for a project mecatl settings file with a non-empty permissions.allow list, and false otherwise", () => {
    const allow = workspace();
    file(
      allow,
      ".mecatl/settings.yaml",
      'permissions:\n  allow:\n    - Read\n    - "Shell(go test)"\n',
    );
    expect(hasProjectAuthority(allow)).toBe(true);

    const local = workspace();
    file(
      local,
      ".mecatl/settings.local.yaml",
      "permissions:\n  allow: [Glob]\n",
    );
    expect(hasProjectAuthority(local)).toBe(true);

    // Deny/ask rules apply trusted or not — a settings file's presence alone
    // is not authority.
    const denyOnly = workspace();
    file(
      denyOnly,
      ".mecatl/settings.yaml",
      "permissions:\n  deny:\n    - Shell\n  ask:\n    - Write\n  allow: []\n",
    );
    expect(hasProjectAuthority(denyOnly)).toBe(false);

    const settingsLink = workspace();
    const target = file(
      settingsLink,
      "outside/settings.yaml",
      "permissions:\n  allow:\n    - Read\n",
    );
    mkdirSync(join(settingsLink, ".mecatl"), { recursive: true });
    symlinkSync(target, join(settingsLink, ".mecatl", "settings.yaml"));
    expect(hasProjectAuthority(settingsLink)).toBe(false);
  });

  it("is true for a project Claude settings.json with a non-empty permissions.allow list, and false otherwise", () => {
    const allow = workspace();
    file(
      allow,
      ".claude/settings.json",
      JSON.stringify({ permissions: { allow: ["Bash(go test:*)"] } }),
    );
    expect(hasProjectAuthority(allow)).toBe(true);

    const local = workspace();
    file(
      local,
      ".claude/settings.local.json",
      JSON.stringify({ permissions: { allow: ["Read"] } }),
    );
    expect(hasProjectAuthority(local)).toBe(true);

    const blank = workspace();
    file(
      blank,
      ".claude/settings.json",
      JSON.stringify({ permissions: { allow: ["", "  "], deny: ["Shell"] } }),
    );
    expect(hasProjectAuthority(blank)).toBe(false);

    const broken = workspace();
    file(broken, ".claude/settings.json", "{ not json");
    expect(hasProjectAuthority(broken)).toBe(false);
  });
});

describe("yamlHasPermissionsAllow", () => {
  it("reads a top-level permissions block with at least one non-blank allow item", () => {
    expect(
      yamlHasPermissionsAllow("permissions:\n  allow:\n    - Read\n"),
    ).toBe(true);
    expect(
      yamlHasPermissionsAllow(
        "# comment\nposture: strict\npermissions:\n  deny:\n    - Shell\n  allow:\n    - 'Shell(go test)' # trailing\n",
      ),
    ).toBe(true);
    expect(
      yamlHasPermissionsAllow('permissions:\n  allow: ["Glob", Read]'),
    ).toBe(true);
    expect(
      yamlHasPermissionsAllow("permissions:\n\tallow:\n\t\t- Read\n"),
    ).toBe(true);
  });

  it("is fail-safe false for empty, blank-item, nested-elsewhere or unparseable shapes", () => {
    expect(yamlHasPermissionsAllow("")).toBe(false);
    expect(yamlHasPermissionsAllow(undefined as unknown as string)).toBe(false);
    expect(yamlHasPermissionsAllow("permissions:\n  allow:\n")).toBe(false);
    expect(yamlHasPermissionsAllow("permissions:\n  allow: []\n")).toBe(false);
    expect(yamlHasPermissionsAllow("permissions:\n  allow: ['']\n")).toBe(
      false,
    );
    expect(
      yamlHasPermissionsAllow("permissions:\n  allow:\n    - ''\n    - \"\"\n"),
    ).toBe(false);
    // An allow list under some OTHER top-level key is not permissions.allow.
    expect(yamlHasPermissionsAllow("subagent:\n  allow:\n    - Read\n")).toBe(
      false,
    );
    // Deny-only, then a new top-level key ends the block.
    expect(
      yamlHasPermissionsAllow(
        "permissions:\n  deny:\n    - Shell\nother:\n  allow:\n    - Read\n",
      ),
    ).toBe(false);
    // A nested mapping under allow (not a list item) is not read with confidence.
    expect(
      yamlHasPermissionsAllow("permissions:\n  allow:\n    tool: Read\n"),
    ).toBe(false);
    // Comments are not items.
    expect(
      yamlHasPermissionsAllow("permissions:\n  allow:\n    # - Read\n"),
    ).toBe(false);
  });
});

describe("jsonHasPermissionsAllow", () => {
  it("accepts a non-blank string item and rejects everything else", () => {
    expect(jsonHasPermissionsAllow('{"permissions":{"allow":["Read"]}}')).toBe(
      true,
    );
    expect(
      jsonHasPermissionsAllow('{"permissions":{"allow":["", "Read"]}}'),
    ).toBe(true);
    expect(jsonHasPermissionsAllow('{"permissions":{"allow":[]}}')).toBe(false);
    expect(jsonHasPermissionsAllow('{"permissions":{"allow":[" "]}}')).toBe(
      false,
    );
    expect(jsonHasPermissionsAllow('{"permissions":{"allow":[42]}}')).toBe(
      false,
    );
    expect(jsonHasPermissionsAllow('{"permissions":{"allow":"Read"}}')).toBe(
      false,
    );
    expect(jsonHasPermissionsAllow('{"permissions":{"deny":["Shell"]}}')).toBe(
      false,
    );
    expect(jsonHasPermissionsAllow("{")).toBe(false);
    expect(jsonHasPermissionsAllow("null")).toBe(false);
  });
});

describe("trustAnchor", () => {
  it("is empty for an unresolvable workspace and stable for the same one", () => {
    expect(trustAnchor(join(fixturesRoot, "missing"))).toBe("");
    expect(trustAnchor("")).toBe("");
    const ws = workspace();
    const first = trustAnchor(ws);
    expect(first).toMatch(/^[0-9a-f]{64}$/);
    expect(trustAnchor(ws)).toBe(first);
  });

  it("hashes anchor.go's transcript byte-for-byte: the soul is always a member, absent or present", () => {
    const empty = workspace();
    expect(trustAnchor(empty)).toBe(expectedAnchor([SOUL_ABSENT]));

    const withSoul = workspace();
    file(withSoul, PROJECT_SOUL_REL, "# be kind\n");
    expect(trustAnchor(withSoul)).toBe(
      expectedAnchor([{ rel: PROJECT_SOUL_REL, bytes: "# be kind\n" }]),
    );
  });

  it("folds every regular file under the anchor dirs by its root-relative slash path, bytewise-sorted", () => {
    const ws = workspace();
    file(ws, PROJECT_SOUL_REL, "soul");
    file(ws, ".mecatl/skills/review/SKILL.md", "skill");
    file(ws, ".claude/agents/explorer.md", "agent");
    file(ws, ".mecatl/commands/ship.md", "command");
    expect(trustAnchor(ws)).toBe(
      expectedAnchor([
        { rel: PROJECT_SOUL_REL, bytes: "soul" },
        { rel: ".mecatl/skills/review/SKILL.md", bytes: "skill" },
        { rel: ".claude/agents/explorer.md", bytes: "agent" },
        { rel: ".mecatl/commands/ship.md", bytes: "command" },
      ]),
    );
  });

  it("drifts when a member appears or changes, and NOT when permission rules change", () => {
    const ws = workspace();
    file(ws, PROJECT_SOUL_REL, "soul");
    const before = trustAnchor(ws);

    // Allow rules churn on nearly every commit; they are deliberately not folded.
    file(ws, ".mecatl/settings.yaml", "permissions:\n  allow:\n    - Read\n");
    file(
      ws,
      ".claude/settings.json",
      JSON.stringify({ permissions: { allow: ["Read"] } }),
    );
    expect(trustAnchor(ws)).toBe(before);

    file(ws, ".claude/skills/new/SKILL.md", "v1");
    const added = trustAnchor(ws);
    expect(added).not.toBe(before);

    file(ws, ".claude/skills/new/SKILL.md", "v2");
    expect(trustAnchor(ws)).not.toBe(added);
  });

  it("treats a symlinked soul or file as absent and never traverses a symlinked dir", () => {
    const ws = workspace();
    const soulTarget = file(ws, "outside/soul.md", "soul");
    mkdirSync(join(ws, ".mecatl"), { recursive: true });
    symlinkSync(soulTarget, join(ws, ".mecatl", "soul.md"));
    // O_NOFOLLOW / Lstat: the link is not a regular file, so the soul reads absent.
    expect(trustAnchor(ws)).toBe(expectedAnchor([SOUL_ABSENT]));

    file(ws, "outside/dir/agent.md", "agent");
    mkdirSync(join(ws, ".mecatl", "agents"), { recursive: true });
    symlinkSync(
      join(ws, "outside", "dir"),
      join(ws, ".mecatl", "agents", "linked"),
    );
    symlinkSync(
      join(ws, "outside", "dir", "agent.md"),
      join(ws, ".mecatl", "agents", "link.md"),
    );
    expect(trustAnchor(ws)).toBe(expectedAnchor([SOUL_ABSENT]));
  });

  it("hashes each member over its first MiB only", () => {
    const ws = workspace();
    const mib = 1 << 20;
    const head = Buffer.alloc(mib, 0x61);
    file(
      ws,
      ".claude/commands/big.md",
      Buffer.concat([head, Buffer.from("tail-1")]),
    );
    const anchor = trustAnchor(ws);
    expect(anchor).toBe(
      expectedAnchor([
        SOUL_ABSENT,
        { rel: ".claude/commands/big.md", bytes: head },
      ]),
    );
    // A change beyond the cap is invisible to the anchor.
    file(
      ws,
      ".claude/commands/big.md",
      Buffer.concat([head, Buffer.from("tail-2")]),
    );
    expect(trustAnchor(ws)).toBe(anchor);
    // A change inside it is not.
    const other = Buffer.alloc(mib, 0x62);
    file(
      ws,
      ".claude/commands/big.md",
      Buffer.concat([other, Buffer.from("tail-2")]),
    );
    expect(trustAnchor(ws)).not.toBe(anchor);
  });
});

describe("trustDecision", () => {
  it("names the four decisions", () => {
    expect([...TRUST_DECISIONS]).toEqual([
      "trusted",
      "once",
      "drifted",
      "untrusted",
    ]);
  });

  it("reports the posture floor first: trusted and above trust regardless of the registry", () => {
    for (const posture of ["trusted", "auto", "yolo"]) {
      for (const trustProject of [false, true]) {
        expect(
          trustDecision(
            { posture, trustProject },
            { trustOnce: false, trustDrifted: true },
          ),
          posture,
        ).toEqual({ decision: "trusted", source: "posture" });
      }
    }
  });

  it("folds the registry inputs in the daemon's order under strict", () => {
    const strict = (trustProject: boolean) => ({
      posture: "strict",
      trustProject,
    });
    expect(trustDecision(strict(false))).toEqual({
      decision: "untrusted",
      source: "none",
    });
    expect(trustDecision(strict(true))).toEqual({
      decision: "trusted",
      source: "studio",
    });
    // A drifted saved grant is withheld: the spawn got no flag, so nobody granted it.
    expect(trustDecision(strict(true), { trustDrifted: true })).toEqual({
      decision: "drifted",
      source: "none",
    });
    // Trust-once is a fresh explicit answer and out-ranks drift.
    expect(
      trustDecision(strict(true), { trustOnce: true, trustDrifted: true }),
    ).toEqual({ decision: "once", source: "studio" });
    expect(trustDecision(strict(false), { trustOnce: true })).toEqual({
      decision: "once",
      source: "studio",
    });
    // An unknown posture ranks below strict and never implies trust.
    expect(trustDecision({ posture: "bogus", trustProject: false })).toEqual({
      decision: "untrusted",
      source: "none",
    });
  });
});
