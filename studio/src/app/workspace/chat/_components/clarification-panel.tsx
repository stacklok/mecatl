"use client";

import { CircleHelp } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import type { ClarificationRequest } from "@/features/agent";

export function ClarificationPanel({
  clarification,
  onRespond,
}: {
  clarification: ClarificationRequest;
  onRespond: (response: string) => void;
}) {
  const [input, setInput] = useState("");
  const [choices, setChoices] = useState<string[]>([]);

  useEffect(() => {
    const lines = clarification.question.split("\n").filter((l) => l.trim());
    const numbered = lines.filter((l) => /^\d+[.)]\s/.test(l.trim()));
    if (numbered.length > 0) {
      setChoices(numbered.map((l) => l.replace(/^\d+[.)]\s*/, "").trim()));
    }
  }, [clarification.question]);

  const questionText = clarification.question
    .split("\n")
    .filter((l) => !/^\d+[.)]\s/.test(l.trim()))
    .join("\n")
    .trim();

  return (
    <div className="rounded-2xl border border-zinc-300 bg-background p-4 dark:border-zinc-700">
      <div className="flex items-center gap-2 mb-3">
        <CircleHelp className="size-4 text-info" />
        <span className="text-sm font-semibold text-info">
          Clarification needed
        </span>
      </div>
      <p className="text-sm mb-3">{questionText || clarification.question}</p>
      {choices.length > 0 && (
        <div className="space-y-1.5 mb-3">
          {choices.map((choice, i) => (
            <button
              key={choice}
              type="button"
              onClick={() => onRespond(String(i + 1))}
              className="flex items-center gap-3 w-full rounded-lg border border-info/20 bg-background px-3 py-2.5 text-left hover:border-info/50 transition-colors"
            >
              <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-info/20 text-xs font-bold text-info">
                {i + 1}
              </span>
              <span className="text-sm font-medium">{choice}</span>
            </button>
          ))}
          <button
            type="button"
            onClick={() => {
              const el = document.getElementById("clarify-input");
              el?.focus();
            }}
            className="flex items-center gap-3 w-full rounded-lg border border-border bg-background px-3 py-2.5 text-left hover:border-info/30 transition-colors"
          >
            <span className="text-sm text-muted-foreground">Other</span>
          </button>
        </div>
      )}
      <div className="flex gap-2">
        <input
          id="clarify-input"
          type="text"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Type your response..."
          className="flex-1 rounded-lg border border-info/30 bg-background px-3 py-2 text-sm focus:outline-none focus:border-info"
          onKeyDown={(e) => {
            if (e.key === "Enter" && input.trim()) {
              onRespond(input.trim());
              setInput("");
            }
          }}
        />
        <Button
          size="sm"
          onClick={() => {
            if (input.trim()) {
              onRespond(input.trim());
              setInput("");
            }
          }}
          disabled={!input.trim()}
          className="bg-info hover:bg-info/90 text-white"
        >
          Send
        </Button>
      </div>
    </div>
  );
}
