/**
 * The controller's OWN project-trust registry probes — node-only (node:fs,
 * node:crypto), imported by the controller (`scripts/local-controller.mjs`)
 * and its vitest suite, NEVER by browser code (controller-permissions.mjs is
 * the browser-safe sibling the Permissions page imports; this module must
 * not be folded into it, or the client bundle breaks on node built-ins).
 *
 * mecated never prompts for trust; mecatui's pre-TUI prompt decides from the
 * Go composition layer (internal/app ResolveTrust / HasProjectAuthority /
 * RememberTrust over a machine-written ~/.config/mecatl/trust.yaml). Studio
 * cannot call Go, so its controller keeps its own registry — `trustProject`
 * + `trustAnchor` in permissions.json — and mirrors the daemon's two probes
 * here in JavaScript:
 *
 *   hasProjectAuthority ← internal/adapter/workspacetrust/authority.go
 *   trustAnchor         ← internal/adapter/workspacetrust/anchor.go
 *
 * Both are FAIL-SAFE in the untrusted direction: an unresolvable workspace or
 * any IO/parse failure reads "no authority" / "" (no stable anchor), which at
 * worst skips a prompt or reads a remembered grant as drifted — never grants.
 * This registry NEVER reads or writes trust.yaml or settings.yaml; the daemon
 * may still trust the workspace from ITS registry (trust.yaml, the user-global
 * `trustedWorkspaces:` list) without Studio knowing, which the UI states.
 */

import { createHash } from "node:crypto";
import {
  closeSync,
  constants,
  lstatSync,
  openSync,
  readdirSync,
  readSync,
  realpathSync,
  statSync,
} from "node:fs";
import { join } from "node:path";
import { postureImpliesTrust } from "./controller-permissions.mjs";

/** The project soul, relative to the workspace root (soulselect.go). */
export const PROJECT_SOUL_REL = ".mecatl/soul.md";

/**
 * The project-tier directories the identity anchor folds and the authority
 * probe scans: agent defs (agentfs ProjectDir*), skill defs (skillfs
 * ProjectDir*) and slash commands (prompt.DefaultCommandDirs). Sorted, as
 * anchor.go's newAnchorDirs sorts them.
 */
export const ANCHOR_DIRS = Object.freeze(
  [
    ".mecatl/agents",
    ".claude/agents",
    ".mecatl/skills",
    ".claude/skills",
    ".mecatl/commands",
    ".claude/commands",
  ].sort(),
);

/** Project mecatl settings whose `permissions.allow` list is authority. */
const PROJECT_SETTINGS_YAML = [
  ".mecatl/settings.yaml",
  ".mecatl/settings.local.yaml",
];
/** Project Claude settings whose `permissions.allow` list is authority. */
const PROJECT_SETTINGS_JSON = [
  ".claude/settings.json",
  ".claude/settings.local.json",
];

/** anchor.go anchorMaxFileBytes: each member is hashed over its first MiB. */
const ANCHOR_MAX_FILE_BYTES = 1 << 20;
/** anchor.go anchorMaxFiles: the member cap (the soul counts as one). */
const ANCHOR_MAX_FILES = 4096;

/**
 * Resolves the workspace to its real path, or null when it does not resolve
 * (the daemon's realpath step; symmetric with its registry keying).
 * @param {string} workspace
 */
function realWorkspace(workspace) {
  if (typeof workspace !== "string" || workspace === "") return null;
  try {
    const root = realpathSync(workspace);
    return statSync(root).isDirectory() ? root : null;
  } catch {
    return null;
  }
}

/** Whether `path` is a regular file (Lstat: a symlink is NOT a file). */
function isRegularFile(path) {
  try {
    return lstatSync(path).isFile();
  } catch {
    return false;
  }
}

/**
 * Reads up to `limit` bytes of a file WITHOUT following a symlink at the
 * leaf (O_NOFOLLOW, anchor.go osAnchorRead). Returns null on any failure.
 * @param {string} path
 * @param {number} limit
 */
function readBounded(path, limit) {
  let fd = -1;
  try {
    fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    const chunks = [];
    let total = 0;
    const buffer = Buffer.allocUnsafe(64 * 1024);
    while (total < limit) {
      const read = readSync(
        fd,
        buffer,
        0,
        Math.min(buffer.length, limit - total),
        null,
      );
      if (read <= 0) break;
      chunks.push(Buffer.from(buffer.subarray(0, read)));
      total += read;
    }
    return Buffer.concat(chunks, total);
  } catch {
    return null;
  } finally {
    if (fd >= 0) {
      try {
        closeSync(fd);
      } catch {
        // nothing to recover: the bytes are already read or the open failed
      }
    }
  }
}

/** Byte-order comparison (Go sorts strings bytewise; JS sorts UTF-16 units). */
function compareBytes(a, b) {
  return Buffer.compare(Buffer.from(a, "utf8"), Buffer.from(b, "utf8"));
}

/**
 * Visits every regular file under `dir` (recursively, in byte-sorted name
 * order like os.ReadDir), calling `visit(rel)` with the dir-relative slash
 * path. Symlinked directories are leaves, not traversed (WalkDir semantics);
 * an unreadable entry is skipped. `visit` returning false stops the walk.
 * A missing or non-directory root contributes nothing.
 * @param {string} dir
 * @param {(rel: string) => boolean | void} visit
 * @returns {boolean} false when the visitor stopped the walk
 */
function walkRegularFiles(dir, visit) {
  try {
    if (!lstatSync(dir).isDirectory()) return true;
  } catch {
    return true;
  }
  /** @param {string} current @param {string} relPrefix */
  const descend = (current, relPrefix) => {
    let entries;
    try {
      entries = readdirSync(current, { withFileTypes: true });
    } catch {
      return true;
    }
    entries.sort((x, y) => compareBytes(x.name, y.name));
    for (const entry of entries) {
      const rel = relPrefix ? `${relPrefix}/${entry.name}` : entry.name;
      if (entry.isDirectory()) {
        if (!descend(join(current, entry.name), rel)) return false;
      } else if (entry.isFile()) {
        if (visit(rel) === false) return false;
      }
      // symlinks, sockets, devices: skipped (only regular files count)
    }
    return true;
  };
  return descend(dir, "");
}

/**
 * Whether a YAML `allow:` value line carries a non-empty inline flow list
 * (`allow: [Read, "Shell(go test)"]`). Fail-safe false on anything else.
 * @param {string} value
 */
function inlineListHasItem(value) {
  const trimmed = value.trim();
  if (!trimmed.startsWith("[") || !trimmed.endsWith("]")) return false;
  return trimmed
    .slice(1, -1)
    .split(",")
    .some((item) => unquoted(item) !== "");
}

/** A YAML scalar item with surrounding whitespace and one pair of quotes
 *  removed (`'Shell(ls)'` → `Shell(ls)`; `''` → ``). */
function unquoted(item) {
  return item
    .trim()
    .replace(/^["']|["']$/g, "")
    .trim();
}

/**
 * The conservative line-based probe for a project mecatl settings.yaml:
 * true only when a top-level `permissions:` block contains an `allow:` key
 * with at least one non-blank item (an indented `- item` line below it, or a
 * non-empty inline `[...]` list). It deliberately does NOT parse YAML in
 * full — anything it cannot read with confidence is "no allow rules" (the
 * fail-safe direction: at worst a prompt is skipped, never granted).
 * @param {string} text
 */
export function yamlHasPermissionsAllow(text) {
  if (typeof text !== "string" || text.trim() === "") return false;
  const lines = text.split(/\r?\n/);
  let inPermissions = false;
  let allowIndent = -1;
  for (const raw of lines) {
    const line = raw.replace(/\s+#.*$/, "").replace(/\t/g, "  ");
    if (line.trim() === "" || line.trimStart().startsWith("#")) continue;
    const indent = line.length - line.trimStart().length;
    const body = line.trim();
    if (indent === 0) {
      inPermissions = /^permissions\s*:\s*$/.test(body);
      allowIndent = -1;
      continue;
    }
    if (!inPermissions) continue;
    if (allowIndent >= 0) {
      if (indent > allowIndent && body.startsWith("- ")) {
        if (unquoted(body.slice(2)) !== "") return true;
        continue;
      }
      if (indent > allowIndent) continue; // a nested value we do not read
      allowIndent = -1; // the allow list ended (possibly empty)
    }
    const match = /^allow\s*:(.*)$/.exec(body);
    if (match) {
      const value = match[1].trim();
      if (value === "") {
        allowIndent = indent;
      } else if (inlineListHasItem(value)) {
        return true;
      }
    }
  }
  return false;
}

/**
 * The Claude-Code settings.json probe: `{permissions:{allow:[…]}}` with a
 * non-blank string item. Any parse failure is false.
 * @param {string} text
 */
export function jsonHasPermissionsAllow(text) {
  try {
    const parsed = JSON.parse(text);
    const allow = parsed?.permissions?.allow;
    return (
      Array.isArray(allow) &&
      allow.some((item) => typeof item === "string" && item.trim() !== "")
    );
  } catch {
    return false;
  }
}

/**
 * Whether the workspace carries a project AUTHORITY SET a trust grant would
 * admit — the presence probe behind the trust prompt (authority.go
 * hasProjectAuthority). True when any of: the project soul is a regular
 * file; any regular file exists under one of ANCHOR_DIRS; a project
 * `.mecatl/settings{,.local}.yaml` has a non-empty `permissions.allow` list;
 * a project `.claude/settings{,.local}.json` does. A settings file's
 * presence alone is NOT authority (deny/ask apply trusted or not). A
 * workspace with no authority set has nothing to gate, so no prompt.
 * @param {string | null | undefined} workspace
 */
export function hasProjectAuthority(workspace) {
  const root = realWorkspace(workspace);
  if (!root) return false;
  if (isRegularFile(join(root, PROJECT_SOUL_REL))) return true;
  for (const dir of ANCHOR_DIRS) {
    let found = false;
    walkRegularFiles(join(root, dir), () => {
      found = true;
      return false;
    });
    if (found) return true;
  }
  for (const rel of PROJECT_SETTINGS_YAML) {
    const path = join(root, rel);
    if (!isRegularFile(path)) continue;
    const data = readBounded(path, ANCHOR_MAX_FILE_BYTES);
    if (data && yamlHasPermissionsAllow(data.toString("utf8"))) return true;
  }
  for (const rel of PROJECT_SETTINGS_JSON) {
    const path = join(root, rel);
    if (!isRegularFile(path)) continue;
    const data = readBounded(path, ANCHOR_MAX_FILE_BYTES);
    if (data && jsonHasPermissionsAllow(data.toString("utf8"))) return true;
  }
  return false;
}

/**
 * The workspace IDENTITY ANCHOR (anchor.go anchorHash), byte-for-byte: a
 * SHA-256 over the transcript `<rel>\0<sha256hex(first MiB)>\n` per member —
 * or `<rel>\0absent:<rel>\n` for a missing/unreadable one — with members
 * sorted by their root-relative slash path and capped at 4096. The soul is
 * ALWAYS a member (its absence is a stable marker, so gaining a soul later
 * drifts); every regular file under ANCHOR_DIRS is one. Permission-rule
 * files are deliberately NOT folded: allow rules churn on nearly every
 * commit, and hashing them would nag the operator into blind-clicking trust.
 * "" for an unresolvable workspace (a remembered grant then reads drifted).
 * @param {string | null | undefined} workspace
 * @returns {string} lowercase-hex SHA-256, or ""
 */
export function trustAnchor(workspace) {
  const root = realWorkspace(workspace);
  if (!root) return "";
  /** @type {{rel: string, hash: string}[]} */
  const members = [];
  const soulPath = join(root, PROJECT_SOUL_REL);
  const soul = isRegularFile(soulPath)
    ? readBounded(soulPath, ANCHOR_MAX_FILE_BYTES)
    : null;
  members.push({
    rel: PROJECT_SOUL_REL,
    hash: soul ? createHash("sha256").update(soul).digest("hex") : "absent",
  });
  for (const dir of ANCHOR_DIRS) {
    const dirAbs = join(root, dir);
    walkRegularFiles(dirAbs, (rel) => {
      if (members.length >= ANCHOR_MAX_FILES) return false;
      const data = readBounded(join(dirAbs, rel), ANCHOR_MAX_FILE_BYTES);
      members.push({
        rel: `${dir}/${rel}`,
        hash: data ? createHash("sha256").update(data).digest("hex") : "absent",
      });
    });
  }
  members.sort((a, b) => compareBytes(a.rel, b.rel));
  const transcript = createHash("sha256");
  for (const member of members) {
    transcript.update(member.rel, "utf8");
    transcript.update(Buffer.from([0]));
    transcript.update(
      member.hash === "absent" ? `absent:${member.rel}` : member.hash,
      "utf8",
    );
    transcript.update("\n", "utf8");
  }
  return transcript.digest("hex");
}

/** The trust decisions `/status.trust.decision` can report. */
export const TRUST_DECISIONS = Object.freeze([
  "trusted",
  "once",
  "drifted",
  "untrusted",
]);

/**
 * The controller's resolved project-trust decision for the CURRENT spawn,
 * mirroring the daemon's fold order for the inputs Studio controls:
 *
 *   posture ≥ trusted  ⇒ "trusted"  (mecated's own floor on an interactive
 *                                     root; the anchor is irrelevant)
 *   trustOnce          ⇒ "once"     (a fresh grant, this controller process)
 *   saved + drifted    ⇒ "drifted"  (saved, but withheld: no flag passed)
 *   saved              ⇒ "trusted"
 *   otherwise          ⇒ "untrusted"
 *
 * `source` names who granted it: "posture", "studio" (this registry) or
 * "none". A drifted grant reports "none" because the daemon did NOT receive
 * the flag — the honest label for what the spawn actually got.
 *
 * @param {{posture: string, trustProject: boolean}} permissions
 * @param {{trustOnce?: boolean, trustDrifted?: boolean}} [state]
 * @returns {{decision: "trusted"|"once"|"drifted"|"untrusted", source: "posture"|"studio"|"none"}}
 */
export function trustDecision(
  permissions,
  { trustOnce = false, trustDrifted = false } = {},
) {
  if (postureImpliesTrust(permissions.posture))
    return { decision: "trusted", source: "posture" };
  if (trustOnce) return { decision: "once", source: "studio" };
  if (permissions.trustProject && trustDrifted)
    return { decision: "drifted", source: "none" };
  if (permissions.trustProject)
    return { decision: "trusted", source: "studio" };
  return { decision: "untrusted", source: "none" };
}
