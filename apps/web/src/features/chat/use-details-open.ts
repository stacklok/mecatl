// SPDX-License-Identifier: Apache-2.0

import { useCallback, useState } from "react";
import { useExpandDetails } from "../../lib/profile-preferences";

/**
 * The open state of one collapsible transcript detail (a turn's tool rows,
 * a reasoning summary, a failed turn's raw payload). It follows the global
 * "Expand details" preference — so the chat menu's toggle opens or closes
 * every disclosure at once — until the reader toggles this one by hand, and
 * re-follows the preference on its next flip.
 */
export function useDetailsOpen(): [boolean, () => void] {
  const { value: expandDetails } = useExpandDetails();
  const [override, setOverride] = useState<boolean | null>(null);
  // The preference value this instance last followed: a global flip resets
  // the local choice, so the menu toggle always wins (React's "state from
  // the previous render" idiom — no effect, no extra paint of stale state).
  const [followed, setFollowed] = useState(expandDetails);
  if (followed !== expandDetails) {
    setFollowed(expandDetails);
    setOverride(null);
  }
  const open = override ?? expandDetails;
  const toggle = useCallback(() => {
    setOverride((current) => !(current ?? expandDetails));
  }, [expandDetails]);
  return [open, toggle];
}
