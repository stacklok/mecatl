"use client";

import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { cn } from "@/lib/utils";

function getInitials(name: string): string {
  const words = name.trim().split(/[\s/]+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0][0].toUpperCase();
  return `${words[0][0]}${words[words.length - 1][0]}`.toUpperCase();
}

// The brand-tinted initials circle from the console. Initials only — there is no
// photo source in a local single-operator app, and inventing a gravatar lookup
// would put a network call on the shell's critical path.
export function UserAvatar({
  name,
  className,
}: {
  name: string;
  className?: string;
}) {
  return (
    <Avatar className={cn("size-8 md:size-9", className)}>
      <AvatarFallback className="rounded-full bg-brand/10 font-medium text-brand-ink">
        {getInitials(name)}
      </AvatarFallback>
    </Avatar>
  );
}
