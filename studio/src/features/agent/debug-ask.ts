import type { ApprovalChoice, ApprovalRequest } from "./types";

/**
 * The developer-tools FAKE permission ask — the web analogue of the TUI's
 * `/debug-ask` built-in (cmd/mecatui/ui/builtins.go runDebugAsk). It parks
 * a canned long-args Shell ask on the approval panel so its layout, focus,
 * queue badge, and verdict flow can be exercised without a model.
 *
 * Nothing here touches the daemon: the ask is minted locally, flagged
 * `synthetic`, and the chat hook short-circuits its verdict (no
 * `resolveAsk` request) and lets a genuine ask displace it. Offered only
 * while Settings → Labs "Developer tools" is on.
 */

/** The reason line every fake ask carries — it names itself as fake. */
export const DEBUG_ASK_REASON =
  "debug ask (Studio developer tools) — not from the model";

/** The prefix an ask id from this module carries (colon-free, so
 *  `isChildAsk` classifies it as a MAIN ask and Always allow is offered). */
export const DEBUG_ASK_ID_PREFIX = "debug-ask-";

/**
 * The three canned Shell commands `/debug-ask` rotates through, mirroring
 * the TUI's payloads: (a) one very long single-line pipeline, (b) a compound
 * &&/||/; command with pipes and redirections, (c) a heredoc carrying real
 * newlines. Each is injected JSON-encoded as `{"command": …}` so the
 * panel's decoded-args tier reads it exactly like a wire ask.
 */
export const DEBUG_ASK_PAYLOADS: readonly string[] = [
  "find . -name '*.go' -not -path './vendor/*' -print0 | xargs -0 grep -nH 'func Test' | awk -F: '{print $1}' | sort | uniq -c | sort -rn | head -40 | while read -r count file; do printf '%5d  %s\\n' \"$count\" \"$file\"; done | tee /tmp/test-counts.txt | column -t -s' '",
  "git fetch origin main && git rebase origin/main || git merge --abort; cargo build --release 2>&1 | tee /tmp/build.log | grep -E 'error|warning' > /tmp/build-issues.txt; docker compose up -d --wait && curl -fsS http://localhost:8080/healthz || docker compose logs --tail=200",
  [
    "cat <<'EOF' > /tmp/report.md",
    "# Nightly report",
    "",
    "## Summary",
    "",
    "- total: 42",
    "- failed: 3",
    "- skipped: 1",
    "",
    "## Failures",
    "",
    "- pkg/foo: TestBar — timeout after 30s waiting on the fixture server",
    "- pkg/baz: TestQux — golden mismatch (see .scratch/qux.diff)",
    "- pkg/quux: TestCorge — nil dereference on empty input",
    "",
    "## Environment",
    "",
    "Run at $(date -u +%FT%TZ) against the staging workspace (us-east-1).",
    "Runner: nightly-04 · image sha256:9f86d08…",
    "",
    "## Next steps",
    "",
    "Re-run the three failing tests with -count=1 -v and attach the artifacts bundle to the tracker issue.",
    "EOF",
    "printf 'wrote %s (%d bytes)\\n' /tmp/report.md \"$(wc -c < /tmp/report.md)\"",
  ].join("\n"),
];

/** The composer warning when `/debug-ask` runs before a chat exists: the
 *  panel the fake ask shows in belongs to a chat, not the draft view. */
export const DEBUG_ASK_NONE_YET =
  "No chat yet — send a message first, then /debug-ask shows the fake ask here";

/** The warning when an ask (real or fake) is already on screen — the
 *  TUI's dedupe: one fake ask never queues behind another. */
export const DEBUG_ASK_ALREADY_PENDING =
  "A permission ask is already pending — answer it before injecting a fake one";

/**
 * Mints one fake Shell ask. `cycle` picks the canned payload (rotating
 * through `DEBUG_ASK_PAYLOADS`) and salts the id, so a re-injection after a
 * verdict is never mistaken for a re-surfaced known ask. `sessionId` is the
 * daemon session the chat is on ("" on a draft).
 */
export function debugAskRequest(sessionId: string, cycle = 0): ApprovalRequest {
  const command =
    DEBUG_ASK_PAYLOADS[Math.abs(cycle) % DEBUG_ASK_PAYLOADS.length] ??
    DEBUG_ASK_PAYLOADS[0] ??
    "";
  return {
    approvalId: `${DEBUG_ASK_ID_PREFIX}${Date.now()}-${cycle}`,
    sessionId,
    toolName: "Shell",
    description: "Shell needs your approval.",
    details: `${DEBUG_ASK_REASON}\n${command}`,
    reason: DEBUG_ASK_REASON,
    args: JSON.stringify({ command }),
    child: false,
    synthetic: true,
  };
}

/**
 * The queue with every fake ask removed — what a GENUINE ask's arrival does
 * first, so a fake parked during a live run can never hide the daemon's
 * real ask behind it (a run wedged awaiting). Same array when none is fake.
 */
export function withoutSyntheticAsks(
  queue: ApprovalRequest[],
): ApprovalRequest[] {
  return queue.some((ask) => ask.synthetic)
    ? queue.filter((ask) => !ask.synthetic)
    : queue;
}

/** The transcript line recording how a fake ask was answered. */
export function debugAskResolvedNotice(choice: ApprovalChoice): string {
  return `debug ask resolved: ${choice} — nothing was sent to the daemon`;
}
