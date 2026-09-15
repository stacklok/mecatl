import type { ReactNode } from "react";
import { IllustrationError } from "@/components/illustrations/illustration-error";

interface ErrorPageLayoutProps {
  title: string;
  children: ReactNode;
  actions?: ReactNode;
}

export function ErrorPageLayout({
  title,
  children,
  actions,
}: ErrorPageLayoutProps) {
  return (
    // bg-background/text-foreground are explicit: an error boundary can render
    // outside the themed shell (over the dark teal backdrop), where inherited
    // colors come out near-black-on-dark. Painting our own surface keeps the
    // page legible in both light and dark modes.
    <div className="flex items-center justify-center min-h-[80vh] bg-background text-foreground">
      <div className="flex flex-col items-center text-center gap-4 max-w-md">
        <IllustrationError className="size-40" />

        <div className="space-y-2">
          <h2 className="text-2xl font-bold tracking-tight">{title}</h2>
          <p className="text-muted-foreground">{children}</p>
        </div>

        {actions && <div className="flex gap-2 mt-2">{actions}</div>}
      </div>
    </div>
  );
}
