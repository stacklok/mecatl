// SPDX-License-Identifier: Apache-2.0

import { Link, useRouter } from "@tanstack/react-router";
import { Component, type ReactNode, useEffect, useRef } from "react";
import { Button } from "../ui/button";

function useHeadingFocus() {
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);
  return heading;
}

function BrandMark() {
  return (
    <span
      aria-hidden="true"
      className="block size-10 shrink-0 bg-brand [mask-image:url(/stacklok-logo-mark.svg)] [mask-position:center] [mask-repeat:no-repeat] [mask-size:contain]"
    />
  );
}

function ErrorActions({ retry, reload = false }: { retry?: () => void; reload?: boolean }) {
  return (
    <div className="mt-7 flex flex-wrap items-center justify-center gap-3">
      {retry && (
        <Button className="min-h-11" onClick={retry} variant="action">
          {reload ? "Reload Studio" : "Try again"}
        </Button>
      )}
      <Button asChild className="min-h-11" variant="outline">
        {reload ? (
          <a href="/workspace/chat">Go to Chats</a>
        ) : (
          <Link search={{ sessionId: undefined }} to="/workspace/chat">
            Go to Chats
          </Link>
        )}
      </Button>
    </div>
  );
}

function ErrorMessage({
  title,
  copy,
  retry,
  reload,
}: {
  title: string;
  copy: string;
  retry?: () => void;
  reload?: boolean;
}) {
  const heading = useHeadingFocus();
  return (
    <div className="mx-auto flex w-full max-w-md flex-col items-center px-5 py-12 text-center">
      <BrandMark />
      <h1
        className="mt-6 text-2xl font-semibold tracking-tight focus:outline-none"
        ref={heading}
        tabIndex={-1}
      >
        {title}
      </h1>
      <p className="mt-3 text-sm leading-6 text-muted-foreground">{copy}</p>
      <ErrorActions reload={reload} retry={retry} />
    </div>
  );
}

/** A child route failure keeps the workspace navigation and status in place. */
export function RouteErrorPage({ reset }: { error: unknown; reset: () => void }) {
  const router = useRouter();
  return (
    <section className="flex h-full min-h-0 items-center overflow-y-auto bg-background text-foreground">
      <ErrorMessage
        copy="This page could not be shown. Try again or return to Chats."
        retry={() => {
          reset();
          void router.invalidate({ sync: true });
        }}
        title="Something went wrong"
      />
    </section>
  );
}

/** Unknown application paths receive a client-rendered destination. */
export function NotFoundPage() {
  return (
    <div className="flex min-h-dvh flex-col bg-background pt-[env(safe-area-inset-top)] text-foreground pb-[env(safe-area-inset-bottom)]">
      <header className="border-b border-border px-5 py-4">
        <Link
          className="inline-flex min-h-11 items-center gap-3 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          search={{ sessionId: undefined }}
          to="/workspace/chat"
        >
          <BrandMark />
          <span className="font-semibold">Mecatl Studio</span>
        </Link>
      </header>
      <main className="flex min-h-0 flex-1 items-center">
        <ErrorMessage copy="The page you requested could not be found." title="Page not found" />
      </main>
    </div>
  );
}

function RootErrorFallback({ reload }: { reload: () => void }) {
  return (
    <main className="flex min-h-dvh items-center bg-background pt-[env(safe-area-inset-top)] text-foreground pb-[env(safe-area-inset-bottom)]">
      <ErrorMessage
        copy="Studio could not open this page. Reload it or return to Chats."
        reload
        retry={reload}
        title="Something went wrong"
      />
    </main>
  );
}

/** Catches failures that happen before a route boundary can mount. */
export class RootErrorBoundary extends Component<
  { children: ReactNode; reload?: () => void },
  { failed: boolean }
> {
  override state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  override render() {
    if (this.state.failed) {
      return <RootErrorFallback reload={this.props.reload ?? (() => window.location.reload())} />;
    }
    return this.props.children;
  }
}

/** Shared by the production router and route-level recovery tests. */
export const studioRouterOptions = {
  defaultErrorComponent: RouteErrorPage,
  defaultNotFoundComponent: NotFoundPage,
  defaultPreload: "intent",
  notFoundMode: "root",
} as const;
