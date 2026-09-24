// SPDX-License-Identifier: Apache-2.0

export interface DeliveryProjection {
  delivery: {
    fireId: string;
    kind: "started" | "completed";
    scheduleName: string;
    stop?: string;
  };
  text: string;
}

export function projectDeliveryNote(text: string): DeliveryProjection | undefined {
  const opener = "<<<UNTRUSTED\n";
  const closer = "\n<<<UNTRUSTED\n";
  if (!text.startsWith(opener) || !text.endsWith(closer)) return undefined;
  const content = text.slice(opener.length, -closer.length);
  // FenceUntrusted neutralizes markers inside its body, so an additional
  // literal marker means this is not a canonical renderer output.
  if (content.includes("<<<UNTRUSTED")) return undefined;

  const completed = projectCompletedNote(content);
  if (completed) return completed;
  // An ambiguous completed note must not fall through and become a forged
  // started note when its untrusted body ends with a started-shaped suffix.
  if (content.includes(") completed with stop reason: ")) return undefined;

  const start = /^\[scheduled task ([\s\S]+) started \(fire ([^)\n]+)\)\]$/.exec(content);
  if (!start) return undefined;
  const [, scheduleName, fireId] = start;
  if (!scheduleName?.trim() || !fireId?.trim()) return undefined;
  return { delivery: { fireId, kind: "started", scheduleName }, text: "" };
}

function projectCompletedNote(content: string): DeliveryProjection | undefined {
  const prefix = "[scheduled task ";
  const fireMarker = " (fire ";
  const stopMarker = ") completed with stop reason: ";
  if (!content.startsWith(prefix)) return undefined;

  // A schedule name and a fire's output are both untrusted. Either may contain
  // a header-shaped line, so only project a note with one possible header end.
  // An ambiguous note remains ordinary transcript text instead of attributing
  // a model-authored fire ID or stop reason to the scheduler.
  let header: { bodyAt: number; fireId: string; scheduleName: string; stop: string } | undefined;
  let lineAt = 0;
  for (const line of content.split("\n")) {
    const stopAt = line.lastIndexOf(stopMarker);
    const fireAt = stopAt < 0 ? -1 : line.lastIndexOf(fireMarker, stopAt);
    if (line.endsWith("]") && fireAt >= 0 && stopAt > fireAt) {
      const candidate = {
        bodyAt: lineAt + line.length + 1,
        fireId: line.slice(fireAt + fireMarker.length, stopAt),
        scheduleName: content.slice(prefix.length, lineAt + fireAt),
        stop: line.slice(stopAt + stopMarker.length, -1),
      };
      if (candidate.fireId.trim() && candidate.scheduleName.trim() && candidate.stop.trim()) {
        if (header) return undefined;
        header = candidate;
      }
    }
    lineAt += line.length + 1;
  }
  if (!header || header.bodyAt >= content.length) return undefined;
  return {
    delivery: {
      fireId: header.fireId,
      kind: "completed",
      scheduleName: header.scheduleName,
      stop: header.stop,
    },
    text: content.slice(header.bodyAt),
  };
}
