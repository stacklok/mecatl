import { ChevronDown, ChevronUp } from "lucide-react";
import { cn } from "@/lib/utils";

interface ChevronIndicatorProps {
  isOpen: boolean;
  className?: string;
}

export function ChevronIndicator({ isOpen, className }: ChevronIndicatorProps) {
  const iconClassName = cn("size-4", className);
  return isOpen ? (
    <ChevronUp className={iconClassName} />
  ) : (
    <ChevronDown className={iconClassName} />
  );
}
