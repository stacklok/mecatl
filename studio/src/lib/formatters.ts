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

const CRON_DAYS = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];

/** 1 → "1st", 22 → "22nd", 13 → "13th" — day-of-month labels. */
export function ordinal(n: number): string {
  const rem10 = n % 10;
  const rem100 = n % 100;
  if (rem10 === 1 && rem100 !== 11) return `${n}st`;
  if (rem10 === 2 && rem100 !== 12) return `${n}nd`;
  if (rem10 === 3 && rem100 !== 13) return `${n}rd`;
  return `${n}th`;
}

function formatClock(hour: number, minute: number): string {
  const period = hour < 12 ? "AM" : "PM";
  const h12 = hour % 12 === 0 ? 12 : hour % 12;
  return `${h12}:${String(minute).padStart(2, "0")} ${period}`;
}

/**
 * Render a standard 5-field cron expression in plain English for the common
 * shapes our scheduled tasks use (daily/weekly at a time, every N hours or
 * minutes). Anything it doesn't recognise falls back to the raw expression, so
 * the exact cron is still available (shown in a tooltip alongside).
 */
export function describeCron(expr: string): string {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return expr;
  const [min, hour, dom, mon, dow] = parts;
  const everyHour = /^\*\/(\d+)$/.exec(hour);
  const everyMin = /^\*\/(\d+)$/.exec(min);
  const numMin = /^\d+$/.test(min);
  const numHour = /^\d+$/.test(hour);

  if (everyMin && hour === "*" && dom === "*" && mon === "*" && dow === "*") {
    return `Every ${everyMin[1]} minutes`;
  }
  if (everyHour && numMin && dom === "*" && mon === "*" && dow === "*") {
    return `Every ${everyHour[1]} hours`;
  }
  if (numMin && numHour && mon === "*") {
    const time = formatClock(Number(hour), Number(min));
    if (dom === "*") {
      if (dow === "*") return `Daily at ${time}`;
      if (dow === "1-5") return `Weekdays at ${time}`;
      if (/^[0-6]$/.test(dow))
        return `Weekly on ${CRON_DAYS[Number(dow)]} at ${time}`;
    } else if (dow === "*" && /^\d{1,2}$/.test(dom)) {
      const day = Number(dom);
      if (day >= 1 && day <= 31)
        return `Monthly on the ${ordinal(day)} at ${time}`;
    }
  }
  return expr;
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
