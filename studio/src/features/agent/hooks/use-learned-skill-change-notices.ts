"use client";

import { useRouter } from "next/navigation";
import { useEffect, useRef } from "react";
import { toast } from "sonner";
import { listLearnedSkillChanges } from "@/lib/harness/learned-skills";
import { onRunFinished } from "../run-signals";
import { useRuntimeStatus } from "../runtime-status";

/**
 * Post-run learned-skill change receipts — mecatui's `ListSkillChangesCmd`
 * on every `ResultMsg` plus its "N learned-skill change receipt(s)
 * available — open /skills" status line, as one toast per run that produced
 * new receipts, with an action that opens the Skills page's Learned view.
 *
 * The daemon's receipt log (`GET /v1/skills/learned/changes`) is
 * append-only and OLDEST-FIRST, and its `cursor` is a receipt id (exclusive
 * — the page after that receipt), so "what is new" is exact: on mount the
 * hook walks to the end of the log once and remembers the newest id WITHOUT
 * announcing (pre-existing receipts are not news), then on every observed
 * run terminal it lists from that id forward, announces the count, and
 * advances. A bounded history can prune the remembered id out from under
 * the cursor; that (or any other failure) silently re-seeds instead of
 * guessing — the receipts are decoration, the Skills page is the surface.
 *
 * Gated on `serverCapabilities.learned_skills` (an older daemon has no
 * receipt log) and on a live connection. Mount once per page.
 */

/** Where the toast's action lands: the Skills page's Learned view. */
export const LEARNED_SKILLS_VIEW_HREF = "/workspace/skills?view=learned";

/** The daemon's `MaxSkillPageSize` (engine/learning/skill.go). */
const RECEIPT_PAGE_SIZE = 200;
/** A walk longer than this is abandoned unannounced (32 768-receipt
 *  histories are the bound; a real log is a handful). */
const MAX_RECEIPT_PAGES = 50;

export function receiptNoticeText(count: number): string {
  return `${count} learned-skill change receipt${count === 1 ? "" : "s"} available`;
}

interface ReceiptWalk {
  /** Receipts passed after the starting id. */
  count: number;
  /** The newest receipt id seen ("" when the log is empty). */
  last: string;
  /** False when the page bound stopped the walk before the log's end. */
  complete: boolean;
}

/** Lists forward from `after` (exclusive; "" = the start) to the log's end. */
export async function walkLearnedSkillReceipts(
  after: string,
  signal?: AbortSignal,
): Promise<ReceiptWalk> {
  let cursor = after;
  let count = 0;
  let last = after;
  for (let page = 0; page < MAX_RECEIPT_PAGES; page++) {
    const { changes, nextCursor } = await listLearnedSkillChanges(
      { cursor, limit: RECEIPT_PAGE_SIZE },
      signal,
    );
    count += changes.length;
    const newest = changes.at(-1)?.id;
    if (newest) last = newest;
    if (!nextCursor) return { count, last, complete: true };
    cursor = nextCursor;
  }
  return { count, last, complete: false };
}

export function useLearnedSkillChangeNotices(): void {
  const { connected, serverCapabilities } = useRuntimeStatus();
  const supported = connected && serverCapabilities.learned_skills === true;
  const router = useRouter();
  const routerRef = useRef(router);
  routerRef.current = router;

  useEffect(() => {
    if (!supported) return;
    const controller = new AbortController();
    const { signal } = controller;
    // The newest receipt already accounted for; null = not seeded (nothing
    // is announced until a seed lands, so a failed seed can never inflate
    // the next count with receipts that predate this page).
    let lastSeen: string | null = null;
    // Walks are serialized: a run ending while the seed is in flight lists
    // AFTER the seed, so its receipts are counted exactly once.
    let chain: Promise<void> = Promise.resolve();
    const enqueue = (task: () => Promise<void>) => {
      chain = chain.then(task, task);
    };

    const seed = async () => {
      try {
        const walk = await walkLearnedSkillReceipts("", signal);
        if (signal.aborted) return;
        lastSeen = walk.complete ? walk.last : null;
      } catch {
        // Decoration: stay unseeded, try again on the next run terminal.
      }
    };

    const announce = async () => {
      if (signal.aborted) return;
      if (lastSeen === null) {
        await seed();
        return;
      }
      try {
        const walk = await walkLearnedSkillReceipts(lastSeen, signal);
        if (signal.aborted) return;
        if (!walk.complete) {
          lastSeen = null;
          return;
        }
        if (walk.count === 0) return;
        lastSeen = walk.last;
        toast.info(receiptNoticeText(walk.count), {
          duration: 10_000,
          action: {
            label: "Open Skills",
            onClick: () => routerRef.current.push(LEARNED_SKILLS_VIEW_HREF),
          },
        });
      } catch {
        // A pruned cursor or a transient failure: re-seed on the next run
        // rather than announce a guess.
        lastSeen = null;
      }
    };

    enqueue(seed);
    const stop = onRunFinished(() => enqueue(announce));
    return () => {
      controller.abort();
      stop();
    };
  }, [supported]);
}
