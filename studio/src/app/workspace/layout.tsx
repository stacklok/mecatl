import { TopNav } from "@/components/shell/top-nav";
import { ConnectionStatusBanner } from "@/features/agent/connection-status-banner";
import { RuntimeStatusProvider } from "@/features/agent/runtime-status";
import { StorageHealthBanner } from "@/features/agent/storage-health-banner";
import { WorkspaceTrustBanner } from "@/features/agent/workspace-trust-banner";
import { ShortcutsProvider } from "@/lib/shortcuts/use-shortcuts";

/**
 * The workspace shell: a dark radial gradient carrying the top navigation
 * bar, with all five surfaces rendered inside one rounded card that follows
 * the theme. The gradient reads the `--shell-gradient-*` tokens
 * (globals.css): the shipped Stacklok green, deepened in dark, and restyled
 * by a named palette (`data-palette` on <html>) — the card interior themes
 * on its own tokens.
 *
 * `RuntimeStatusProvider` stays outermost: its offline banner renders above
 * the top nav at full width. Workspace sections manage their own scrolling
 * and padding inside the card (`h-full overflow-y-auto …`).
 */
export default function WorkspaceLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <RuntimeStatusProvider>
      <ShortcutsProvider>
        {/* The design's radial gradient over the shell tokens; the .dark and
            [data-palette] blocks in globals.css supply the deepened / palette
            stops, so no dark: variant is needed here.
            Top padding tracks the status-bar safe area: the installed iOS
            PWA (black-translucent status bar + viewport-fit cover) draws
            under the clock, so the nav must start below it while the
            gradient still paints behind it. */}
        <div className="flex h-dvh min-w-0 flex-col bg-[radial-gradient(120%_140%_at_20%_30%,var(--shell-gradient-start)_0%,var(--shell-gradient-mid)_50%,var(--shell-gradient-end)_100%)] pt-[env(safe-area-inset-top)]">
          {/* Storage health rides the same full-width banner band as the
              runtime status banners: visible from every surface, because a
              degraded store shows up as chats missing from the sidebar. */}
          <StorageHealthBanner />
          {/* The SDK's own view of the session feed: a durable watch
              reconnecting from its last cursor, or a credential the feed was
              refused with while the 5 s daemon probe still passes. Quiet
              whenever the runtime banners above already own the band. */}
          <ConnectionStatusBanner />
          {/* The first-encounter / drift trust prompt (mecatui's pre-TUI
              "trust / trust once / no", as a banner): the same band, so a
              withheld project soul/agents/allow rules are never silent. */}
          <WorkspaceTrustBanner />
          <TopNav />
          {/* relative makes the card the containing block for absolutely-
              positioned descendants with no positioned ancestor of their own
              — notably the hidden form-integration checkbox Radix renders
              beside each Switch inside a <form>. Without it those boxes
              resolve to the document and grow the page itself. */}
          {/* On mobile the card goes full-bleed — no gradient margin, only
              the top corners stay rounded where it meets the nav band — so
              content spans the screen; the gradient survives only behind the
              top nav and the tab bar. The inset card is a ≥500px treatment. */}
          <main className="relative min-h-0 flex-1 overflow-hidden rounded-t-xl bg-background text-foreground min-[500px]:mx-3 min-[500px]:mb-3 min-[500px]:rounded-[20px]">
            {children}
          </main>
        </div>
      </ShortcutsProvider>
    </RuntimeStatusProvider>
  );
}
