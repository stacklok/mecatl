// SPDX-License-Identifier: Apache-2.0

import { Brain, GraduationCap, Sparkles } from "lucide-react";

export function StateCard({
  error,
  icon,
  text,
  title,
}: {
  error?: boolean;
  icon?: "memory" | "skill" | "sparkles";
  text: string;
  title?: string;
}) {
  const Icon = icon === "memory" ? Brain : icon === "sparkles" ? Sparkles : GraduationCap;
  return (
    <div
      className={`flex min-h-60 flex-col items-center justify-center rounded-xl border border-dashed p-8 text-center ${error ? "border-destructive/40 text-destructive" : ""}`}
    >
      {icon && (
        <span className="flex size-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
          <Icon className="size-5" />
        </span>
      )}
      {title && <h2 className="mt-4 font-semibold">{title}</h2>}
      <p className="mt-2 max-w-md text-sm text-muted-foreground">{text}</p>
    </div>
  );
}
