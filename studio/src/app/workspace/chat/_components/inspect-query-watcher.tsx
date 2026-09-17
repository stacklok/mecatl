"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useRef } from "react";

/**
 * Opens the read-only transcript a deep link names: the global search hands
 * out `/workspace/chat/<parent>?inspect=<id>` for a run, and this watcher
 * turns the query into one `onInspect(id)` call, then strips it from the URL
 * so a reload or a Back does not reopen the dialog. It renders nothing.
 *
 * `useSearchParams` needs a Suspense boundary during a production build (a
 * static shell cannot know the query), so the exported component wraps the
 * reader in one; the chat page itself never suspends on it.
 */
function InspectQueryReader({
  onInspect,
}: {
  onInspect: (id: string) => void;
}) {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const inspect = params.get("inspect") ?? "";
  // The latest handler, so a re-render between the URL change and the effect
  // never triggers a second open for the same id.
  const onInspectRef = useRef(onInspect);
  onInspectRef.current = onInspect;

  useEffect(() => {
    if (!inspect) return;
    onInspectRef.current(inspect);
    router.replace(pathname);
  }, [inspect, pathname, router]);

  return null;
}

export function InspectQueryWatcher(props: {
  onInspect: (id: string) => void;
}) {
  return (
    <Suspense fallback={null}>
      <InspectQueryReader {...props} />
    </Suspense>
  );
}
