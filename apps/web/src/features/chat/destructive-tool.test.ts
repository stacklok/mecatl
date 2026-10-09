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
    "db.dropTable",
    "storage/purge_bucket",
    "k8s:deletePod",
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
    "rmdir",
    "undelete",
    "removal",
    "dropbox_upload",
    "purgeable",
    "farm",
    "github__confirm_merge",
    "format_document",
  ])("classifies %j as not destructive", (name) => {
    expect(isDestructiveToolName(name)).toBe(false);
  });

  it("classifies everything the word-boundary regex classified as destructive", () => {
    // Exhaustive over every name of up to three pieces drawn from verbs in several
    // casings, ordinary words, separators, digits, and a non-ASCII letter.
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
    expect(checked).toBe(24 + 24 ** 2 + 24 ** 3);
    expect(legacyDestructive).toBeGreaterThan(1_000);
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
