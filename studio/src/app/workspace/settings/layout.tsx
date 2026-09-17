import {
  SettingsMobileBar,
  SettingsTitle,
} from "./_components/settings-header";
import { SettingsNav } from "./_components/settings-nav";

export default function SettingsLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <div className="flex h-full flex-col">
      {/* Mobile subpages get a chat-style back header, full-bleed so its
          border spans the card; the scroll area below keeps the padding. */}
      <SettingsMobileBar />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
        <div className="space-y-6">
          <SettingsTitle />
          <div className="flex flex-col gap-6 min-[500px]:flex-row min-[500px]:gap-10">
            <SettingsNav />
            {/* Capped at the reading width the shortcuts page established, so
                forms don't stretch across very wide viewports. */}
            <div className="min-w-0 max-w-3xl flex-1 space-y-5">{children}</div>
          </div>
        </div>
      </div>
    </div>
  );
}
