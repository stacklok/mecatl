"use client";

import { Copy, Download, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import {
  DAEMON_LOG_DEFAULT_LINES,
  DAEMON_LOG_DOWNLOAD_URL,
  fetchHarnessDaemonLog,
  type HarnessDaemonLog,
} from "@/lib/harness/client";
import { Note, OfflineNote, SettingsCard } from "./settings-card";

const TITLE = "Logs";

/** Shown when the agent runs elsewhere: its log is kept there, not here. */
export const EXTERNAL_LOG_NOTE =
  "Logs are kept where the agent runs and aren't available here.";

/** Shown when the controller answered but had no log to report. */
export const LOG_UNAVAILABLE_NOTE =
  "No log is available right now. Restart Studio and try again.";

/**
 * Reads the bounded tail once on mount. Deliberately NOT gated on the
 * agent being reachable: the controller answers while its child is down,
 * and a crash-at-start is exactly when the tail matters.
 */
function useDaemonLogTail(enabled: boolean) {
  const [log, setLog] = useState<HarnessDaemonLog | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const next = await fetchHarnessDaemonLog(
        DAEMON_LOG_DEFAULT_LINES,
        signal,
      );
      if (signal?.aborted) return;
      setLog(next);
      setError(null);
    } catch (caught) {
      if (signal?.aborted) return;
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      if (!signal?.aborted) setLoaded(true);
    }
  }, []);

  useEffect(() => {
    if (!enabled) {
      setLoaded(true);
      return;
    }
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [enabled, load]);

  const reload = useCallback(() => load(), [load]);
  return { log, loaded, error, reload };
}

/**
 * The agent's recent log lines as plain text, with Refresh, Copy and a
 * Download of the full file. A start the agent refused is reported above
 * the lines, so a crash-at-start is diagnosable from this page while the
 * agent itself is unreachable. When the agent runs elsewhere there is no
 * log to show.
 */
export function DaemonLogCard() {
  const { connected, mode } = useRuntimeStatus();
  const managed = mode !== "external";
  const { log, loaded, error, reload } = useDaemonLogTail(managed);
  const tailRef = useRef<HTMLElement>(null);

  // The newest lines matter most: keep the viewer pinned to the bottom as
  // the tail changes.
  useEffect(() => {
    if (!log || !tailRef.current) return;
    tailRef.current.scrollTop = tailRef.current.scrollHeight;
  }, [log]);

  if (!managed) {
    return (
      <SettingsCard title={TITLE}>
        <Note>{EXTERNAL_LOG_NOTE}</Note>
      </SettingsCard>
    );
  }
  if (!log) {
    return (
      <SettingsCard title={TITLE}>
        {!loaded ? (
          <Note>Reading the log…</Note>
        ) : !connected ? (
          <OfflineNote />
        ) : (
          <Note>{error ?? LOG_UNAVAILABLE_NOTE}</Note>
        )}
      </SettingsCard>
    );
  }

  const tailText = log.lines.join("\n");
  const empty = log.lines.length === 0;

  const copyTail = () => {
    void navigator.clipboard
      .writeText(tailText)
      .then(() => toast.success("Log copied"))
      .catch(() => toast.error("Couldn't copy — clipboard blocked"));
  };

  const countLine = empty
    ? "No messages yet."
    : `Last ${log.lines.length} line${log.lines.length === 1 ? "" : "s"}.${
        log.truncated ? " Older messages are in the downloaded file." : ""
      }`;

  return (
    <SettingsCard
      title={TITLE}
      description="Recent messages from the agent, useful when reporting a problem."
    >
      <div className="flex flex-col gap-3">
        {log.startupError !== "" && (
          <div
            role="alert"
            data-testid="daemon-log-startup-error"
            className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            <span className="font-medium">The agent could not start: </span>
            <span className="whitespace-pre-wrap break-words">
              {log.startupError}
            </span>
          </div>
        )}

        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-xs text-muted-foreground">
            {countLine}
            {!log.running ? " The agent is not running." : ""}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              className="rounded-full"
              onClick={() => void reload()}
            >
              <RefreshCw className="size-4" />
              Refresh
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="rounded-full"
              onClick={copyTail}
              disabled={empty}
            >
              <Copy className="size-4" />
              Copy
            </Button>
            {log.sizeBytes > 0 ? (
              <Button
                asChild
                variant="outline"
                size="sm"
                className="rounded-full"
              >
                <a
                  href={DAEMON_LOG_DOWNLOAD_URL}
                  download="agent.log"
                  data-testid="daemon-log-download"
                >
                  <Download className="size-4" />
                  Download logs
                </a>
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                className="rounded-full"
                disabled
              >
                <Download className="size-4" />
                Download logs
              </Button>
            )}
          </div>
        </div>
        {/* A labelled, keyboard-reachable scroll region; the text inside is
            rendered as-is (React escapes it) — never as markup. */}
        <section
          ref={tailRef}
          // biome-ignore lint/a11y/noNoninteractiveTabindex: a scroll region must be keyboard-reachable
          tabIndex={0}
          aria-label="Recent log lines"
          data-testid="daemon-log-tail"
          className="max-h-72 overflow-auto rounded-lg border bg-background p-3 text-muted-foreground"
        >
          <pre className="font-mono text-xs whitespace-pre-wrap break-all">
            {tailText === "" ? "No messages yet." : tailText}
          </pre>
        </section>
      </div>
    </SettingsCard>
  );
}
