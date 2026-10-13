// SPDX-License-Identifier: Apache-2.0

import cronstrue from "cronstrue";

export function formatRelativeTime(ts: number): string {
  if (!ts || ts < 1000) return "";
  try {
    const diffMs = Date.now() - ts;
    const diffMin = Math.floor(diffMs / 60000);
    if (diffMin < 1) return "<1m";
    if (diffMin < 60) return `${diffMin}m`;
    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return `${diffHr}h`;
    const diffDay = Math.floor(diffHr / 24);
    return `${diffDay}d`;
  } catch {
    return "";
  }
}

/**
 * Future counterpart of formatRelativeTime: how long until `ts`, as a bare
 * duration ("5m", "2h", "3d") the caller frames ("in 5m"). A due-or-past
 * instant reads "<1m" rather than a negative.
 */
export function formatUntilTime(ts: number): string {
  if (!ts || ts < 1000) return "";
  try {
    const diffMin = Math.floor((ts - Date.now()) / 60000);
    if (diffMin < 1) return "<1m";
    if (diffMin < 60) return `${diffMin}m`;
    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return `${diffHr}h`;
    return `${Math.floor(diffHr / 24)}d`;
  } catch {
    return "";
  }
}

export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

/**
 * A model's context window as the conventional round label ("128k", "200k",
 * "1M", "1.5M") — whole thousands, unlike `formatTokens`, which keeps one
 * decimal for live usage counts. Zero/unknown (the daemon reports 0 when it
 * does not know the window) reads "" so a caller can fall back to its own
 * placeholder ("—" in a table, nothing in a picker row).
 */
export function formatContextWindow(tokens: number): string {
  if (!Number.isFinite(tokens) || tokens <= 0) return "";
  if (tokens >= 1_000_000) {
    return `${(tokens / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  }
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}k`;
  return String(Math.round(tokens));
}

/**
 * An elapsed millisecond count, compact: under one second verbatim in ms
 * ("840ms"), otherwise seconds to one decimal with a redundant ".0" trimmed
 * ("4.1s", "2s"). Mirrors the TUI footer's formatDuration; negatives and
 * non-finite values clamp to "0ms".
 */
export function formatDurationMs(ms: number): string {
  const clamped = Number.isFinite(ms) && ms > 0 ? ms : 0;
  if (clamped < 1000) return `${Math.round(clamped)}ms`;
  const seconds = (clamped / 1000).toFixed(1).replace(/\.0$/, "");
  return `${seconds}s`;
}

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

/**
 * A byte count in binary units, one decimal with a redundant ".0" trimmed
 * ("0 B", "1.5 KB", "20 MB"). Negatives and non-finite values clamp to
 * "0 B".
 */
export function formatBytes(bytes: number): string {
  let value = Number.isFinite(bytes) && bytes > 0 ? bytes : 0;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const rendered = unit === 0 ? String(value) : value.toFixed(1).replace(/\.0$/, "");
  return `${rendered} ${BYTE_UNITS[unit]}`;
}

/** 1 → "1st", 22 → "22nd", 13 → "13th" — day-of-month labels. */
export function ordinal(n: number): string {
  const rem10 = n % 10;
  const rem100 = n % 100;
  if (rem10 === 1 && rem100 !== 11) return `${n}st`;
  if (rem10 === 2 && rem100 !== 12) return `${n}nd`;
  if (rem10 === 3 && rem100 !== 13) return `${n}rd`;
  return `${n}th`;
}

/**
 * Render a standard 5-field cron expression in plain English. Anything cronstrue
 * cannot parse falls back to the raw expression, so the exact cron is still
 * available (shown in a tooltip alongside).
 */
export function describeCron(expr: string): string {
  const cron = expr.trim();
  if (cron.split(/\s+/).length !== 5) return expr;
  try {
    return cronstrue.toString(cron, { use24HourTimeFormat: false });
  } catch {
    return expr;
  }
}

export function formatMessageTime(ts: number): string {
  if (!ts || ts < 1000) return "";
  const d = new Date(ts);
  return d.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

/**
 * A full local date and time ("Sep 16, 2026, 9:00 AM") for an ISO instant —
 * the one shared long-form stamp (the schedules run-history table and
 * "Next run"/"Last run" labels, knowledge and memory timestamps), replacing
 * the copies that used to live in each of those features.
 */
export function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}
