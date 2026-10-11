// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { isDestructiveToolName, toolNameTokens } from "./destructive-tool";

// The classifier this replaced, kept only as the no-regression reference.
const wordBoundary = /\b(delete|remove|drop|revoke|destroy|purge|rm)\b/iu;

describe("isDestructiveToolName", () => {
  it.each([
    // Already destructive under the word-boundary regex.
    "delete",
    "Delete",
    "DELETE",
    "remove",
    "drop",
    "revoke",
    "destroy",
    "purge",
    "rm",
    "RM",
    "Remove file",
    "git rm",
    "rm -rf",
    "delete-file",
    "fs.delete",
    "mcp/delete",
    "server:drop",
    "ReMove",
    // #2224: camelCase, PascalCase, snake_case, and MCP names.
    "DeleteFile",
    "deleteFile",
    "delete_file",
    "DELETE_FILE",
    "RemoveBranch",
    "removeBranch",
    "drop_table",
    "DropTable",
    "revokeToken",
    "RevokeAccessKey",
    "destroy_vm",
    "purgeCache",
    "fs_rm",
    "FsRm",
    "HTTPDelete",
    "v2Delete",
    "github__delete_branch",
    "mcp__fs__rm",
    // rmdir deletes a directory.
    "rmdir",
    "Rmdir",
    "RMDIR",
    "fs_rmdir",
    "fs.rmdir",
    "mcp__fs__rmdir",
    // An MCP tool's own name still counts after its server segment is skipped.
    "mcp__remove_bg__delete_image",
    "mcp__files__DeleteFile",
    "mcp__x___drop",
    "db.dropTable",
    "storage/purge_bucket",
    "k8s:deletePod",
    // The word-boundary regex case-folds these compatibility letters: ſ (U+017F) to s
    // and K (U+212A) to k.
    "de\u017Ftroy",
    "revo\u212Ae",
    "DE\u017FTROY_VM",
    "revo\u212Ae_token",
    // Fullwidth letters normalize to ASCII.
    "\uFF44\uFF45\uFF4C\uFF45\uFF54\uFF45",
  ])("classifies %j as destructive", (name) => {
    expect(isDestructiveToolName(name)).toBe(true);
  });

  it.each([
    "",
    "Bash",
    "Edit",
    "Read",
    "Write",
    "Glob",
    "Grep",
    "WebFetch",
    "PresentPlan",
    // Verbs inside a longer word are not tokens.
    "confirm",
    "Confirm",
    "format",
    "term",
    "terminal",
    "dropdown",
    "Dropdown",
    "deleted_items_report",
    "DeletedItemsReport",
    "list_removed_files",
    "undelete",
    "removal",
    "dropbox_upload",
    "purgeable",
    "farm",
    "github__confirm_merge",
    "format_document",
    // The mcp__<server>__ segment names a configured server, not an action.
    "mcp__remove_bg__preview",
    "mcp__delete_tracker__list_items",
    "mcp__drop__status",
    "mcp__Purge_Cache__get_stats",
    "mcp__rm__list",
  ])("classifies %j as not destructive", (name) => {
    expect(isDestructiveToolName(name)).toBe(false);
  });

  it("classifies everything the word-boundary regex classified as destructive", () => {
    // Exhaustive over every name of up to three pieces drawn from verbs in several
    // casings, ordinary words, separators, digits, a non-ASCII letter, and the two
    // compatibility letters the regex case-folds into verbs (ſ and the Kelvin sign).
    const pieces = [
      "delete",
      "Delete",
      "DELETE",
      "rm",
      "Rm",
      "RM",
      "drop",
      "Purge",
      "file",
      "File",
      "FS",
      "x",
      "2",
      "_",
      "__",
      "-",
      ".",
      "/",
      ":",
      " ",
      "é",
      "ed",
      "con",
      "down",
      "\u017F",
      "\u212A",
      "de",
      "troy",
      "revo",
      "e",
    ];
    let names = [""];
    let checked = 0;
    let legacyDestructive = 0;
    for (let length = 1; length <= 3; length += 1) {
      names = names.flatMap((name) => pieces.map((piece) => name + piece));
      for (const name of names) {
        checked += 1;
        if (!wordBoundary.test(name)) continue;
        legacyDestructive += 1;
        if (!isDestructiveToolName(name)) throw new Error(`regressed: ${JSON.stringify(name)}`);
      }
    }
    expect(checked).toBe(pieces.length + pieces.length ** 2 + pieces.length ** 3);
    expect(legacyDestructive).toBeGreaterThan(1_000);
  });

  it("classifies everything the regex classified as destructive behind an MCP namespace", () => {
    // Skipping a server segment must not lose a name the regex flagged, whether the
    // segment is a valid server name or not.
    const servers = [
      "fs",
      "remove_bg",
      "delete",
      "Drop_2",
      "x.rm",
      "x.rm.",
      "a-delete",
      "a delete b",
      "rm ",
      "",
    ];
    const tools = ["delete", "rm", "x", ".rm", "rm.x", "drop table", "-purge", "_delete", ""];
    const suffixes = ["", "__", "__rm", ".delete", " rm"];
    let checked = 0;
    let legacyDestructive = 0;
    for (const server of servers) {
      for (const tool of tools) {
        for (const suffix of suffixes) {
          for (const name of [`mcp__${server}__${tool}${suffix}`, `mcp__${server}${suffix}`]) {
            checked += 1;
            if (!wordBoundary.test(name)) continue;
            legacyDestructive += 1;
            if (!isDestructiveToolName(name)) throw new Error(`regressed: ${JSON.stringify(name)}`);
          }
        }
      }
    }
    expect(checked).toBe(servers.length * tools.length * suffixes.length * 2);
    expect(legacyDestructive).toBeGreaterThan(50);
  });
});

describe("toolNameTokens", () => {
  it.each([
    ["DeleteFile", ["deletefile", "delete", "file"]],
    ["delete_file", ["delete", "file"]],
    ["github__delete_branch", ["github", "delete", "branch"]],
    ["fs.delete", ["fs", "delete"]],
    ["k8s:deletePod", ["k8s", "deletepod", "delete", "pod"]],
    ["HTTPDelete", ["httpdelete", "http", "delete"]],
    ["v2Delete", ["v2delete", "v2", "delete"]],
    ["Remove file", ["remove", "file"]],
    ["confirm", ["confirm"]],
    ["", []],
  ])("splits %j", (name, tokens) => {
    expect(toolNameTokens(name)).toEqual(tokens);
  });
});
