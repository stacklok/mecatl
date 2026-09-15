import { TopNav } from "@/components/shell/top-nav";
import { RuntimeStatusProvider } from "@/features/agent/runtime-status";
import { StorageHealthBanner } from "@/features/agent/storage-health-banner";
import { ShortcutsProvider } from "@/lib/shortcuts/use-shortcuts";

/**
 * The workspace shell: a fixed dark-green radial gradient carrying the top
 * navigation bar, with all five surfaces rendered inside one rounded card
 * that follows the theme. The gradient itself is a fixed brand colour —
 * identical in light and dark themes — so only the card interior themes.
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
        {/* The design's green radial gradient; dark mode deepens each stop so
            the shell recedes behind the dark card instead of outglowing it.
            Top padding tracks the status-bar safe area: the installed iOS
            PWA (black-translucent status bar + viewport-fit cover) draws
            under the clock, so the nav must start below it while the
            gradient still paints behind it. */}
        <div className="flex h-dvh min-w-0 flex-col bg-[radial-gradient(120%_140%_at_20%_30%,#006652_0%,#03433e_50%,#06202a_100%)] pt-[env(safe-area-inset-top)] dark:bg-[radial-gradient(120%_140%_at_20%_30%,#023d31_0%,#022723_50%,#02141b_100%)]">
          {/* Storage health rides the same full-width banner band as the
              runtime status banners: visible from every surface, because a
              degraded store shows up as chats missing from the sidebar. */}
          <StorageHealthBanner />
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
