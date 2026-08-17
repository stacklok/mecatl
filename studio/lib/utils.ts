import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// The single source of truth for the serif page-title treatment, mirroring the
// enterprise design system: Merriweather, light weight, softened title ink. The
// SIZE is supplied per call site (a sidebar heading and a page header want the
// same face at different scales), so it is deliberately absent here.
export function pageTitleClass(...extra: ClassValue[]): string {
  return cn("font-serif font-light text-title tracking-tight", ...extra);
}
