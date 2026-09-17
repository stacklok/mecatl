"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useRef } from "react";

/**
 * Selects the Learned tab a deep link names: the learning review queue links
 * a materialized proposal to `/workspace/skills?view=learned`, and this
 * watcher turns that query into one `onView("learned")` call. It renders
 * nothing and leaves the URL alone (the link stays shareable).
 *
 * `useSearchParams` needs a Suspense boundary during a production build (a
 * static shell cannot know the query), so the exported component wraps the
 * reader in one; the skills page itself never suspends on it.
 */
function ViewQueryReader({ onView }: { onView: (view: string) => void }) {
  const params = useSearchParams();
  const view = params.get("view") ?? "";
  const onViewRef = useRef(onView);
  onViewRef.current = onView;

  useEffect(() => {
    if (!view) return;
    onViewRef.current(view);
  }, [view]);

  return null;
}

export function SkillsViewQueryWatcher(props: {
  onView: (view: string) => void;
}) {
  return (
    <Suspense fallback={null}>
      <ViewQueryReader {...props} />
    </Suspense>
  );
}
