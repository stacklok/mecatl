// SPDX-License-Identifier: Apache-2.0

import {
  Bell,
  BellRing,
  Clock,
  Eye,
  History,
  Minus,
  MonitorCog,
  Moon,
  PanelLeft,
  PanelRight,
  Plus,
  SquarePen,
  Sun,
  Zap,
} from "lucide-react";
import { useEffect, useState } from "react";
import { paletteSwatch } from "../../components/palette-swatch";
import { Button } from "../../components/ui/button";
import { OptionField } from "../../components/ui/option-field";
import { Switch } from "../../components/ui/switch";
import {
  type BrowserNotificationPermission,
  browserNotificationPermission,
  requestBrowserNotifications,
  sendBrowserNotification,
} from "../../lib/browser-notifications";
import { BUILT_IN_PALETTES, type Palette, usePalette } from "../../lib/palettes";
import {
  type EnterSendBehavior,
  type SessionListSide,
  type StartOn,
  uiScaleMax,
  uiScaleMin,
  useEnterSendBehavior,
  useSessionListSide,
  useShowToolCalls,
  useStartOn,
  useUiScale,
} from "../../lib/profile-preferences";
import { type Theme, useTheme } from "../../lib/theme";

const THEME_OPTIONS = [
  { icon: Sun, label: "Light", description: "Always light.", value: "light" },
  { icon: Moon, label: "Dark", description: "Always dark.", value: "dark" },
  { icon: MonitorCog, label: "System", description: "Follow this device.", value: "system" },
] as const;

const PALETTE_OPTIONS = BUILT_IN_PALETTES.map((palette) => ({
  icon: paletteSwatch(palette.swatch),
  label: palette.label,
  description: palette.description,
  value: palette.id,
}));

const SIDE_OPTIONS = [
  { icon: PanelLeft, label: "Left", value: "left" },
  { icon: PanelRight, label: "Right", value: "right" },
] as const;

const BUSY_MESSAGE_OPTIONS = [
  { icon: Clock, label: "Queue message", value: "queue" },
  { icon: Zap, label: "Steer agent", value: "steer" },
] as const;

const START_ON_OPTIONS = [
  { icon: SquarePen, label: "New chat", value: "draft" },
  { icon: History, label: "Most recent chat", value: "latest" },
] as const;

export function InterfaceSettings() {
  const theme = useTheme();
  const palette = usePalette();
  const scale = useUiScale();
  const sessionListSide = useSessionListSide();
  const showToolCalls = useShowToolCalls();
  const enterSendBehavior = useEnterSendBehavior();
  const startOn = useStartOn();

  return (
    <section className="rounded-2xl border bg-card p-5 sm:p-6">
      <div className="flex items-center gap-3">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-brand/10 text-brand">
          <MonitorCog aria-hidden="true" className="size-5" />
        </span>
        <div>
          <h2 className="text-lg font-semibold">Appearance</h2>
          <p className="text-sm text-muted-foreground">
            Source: browser appearance and chat preferences. Owner: personal.
          </p>
        </div>
      </div>

      <div className="mt-5 divide-y">
        <PreferenceRow description="Light, dark, or follow this device." label="Theme">
          <OptionField
            label="Theme"
            onChange={(value) => theme.setTheme(value as Theme)}
            options={THEME_OPTIONS}
            value={theme.theme}
          />
        </PreferenceRow>
        <PreferenceRow
          description="Choose Studio's color palette for this browser."
          label="Palette"
        >
          <OptionField
            label="Palette"
            onChange={(value) => palette.setPalette(value as Palette)}
            options={PALETTE_OPTIONS}
            value={palette.palette}
          />
        </PreferenceRow>
        <PreferenceRow description="Sizes text and controls together." label="Interface scale">
          <div className="flex w-44 items-center justify-between gap-2">
            <Button
              aria-label="Decrease interface scale"
              className="min-h-11 min-w-11"
              disabled={scale.value <= uiScaleMin}
              onClick={() => scale.setValue(scale.value - 0.05)}
              size="icon"
              variant="outline"
            >
              <Minus aria-hidden="true" />
            </Button>
            <span className="text-sm tabular-nums">{Math.round(scale.value * 100)}%</span>
            <Button
              aria-label="Increase interface scale"
              className="min-h-11 min-w-11"
              disabled={scale.value >= uiScaleMax}
              onClick={() => scale.setValue(scale.value + 0.05)}
              size="icon"
              variant="outline"
            >
              <Plus aria-hidden="true" />
            </Button>
          </div>
        </PreferenceRow>
        <PreferenceRow
          description="Which side of a desktop window holds chat history."
          label="Session list position"
        >
          <OptionField
            label="Session list position"
            onChange={(value) => sessionListSide.setValue(value as SessionListSide)}
            options={SIDE_OPTIONS}
            value={sessionListSide.value}
          />
        </PreferenceRow>
        <PreferenceRow
          description="Show calls and their returned output inside assistant messages."
          label="Tool activity"
        >
          <label
            className="flex min-h-11 w-44 items-center justify-between gap-2 text-sm"
            htmlFor="show-tool-activity"
          >
            <span className="flex items-center gap-2">
              <Eye aria-hidden="true" className="size-4 text-muted-foreground" />
              Show tools
            </span>
            <Switch
              aria-label="Show tool activity"
              checked={showToolCalls.value}
              id="show-tool-activity"
              onCheckedChange={(checked) => showToolCalls.setValue(checked === true)}
            />
          </label>
        </PreferenceRow>
        <PreferenceRow description="What opens when you go to Chat." label="Start on">
          <OptionField
            label="Start on"
            onChange={(value) => startOn.setValue(value as StartOn)}
            options={START_ON_OPTIONS}
            value={startOn.value}
          />
        </PreferenceRow>
        <PreferenceRow
          description="What Enter does while the agent is replying; Shift+Enter does the opposite."
          label="Message during a run"
        >
          <OptionField
            label="Message during a run"
            onChange={(value) => enterSendBehavior.setValue(value as EnterSendBehavior)}
            options={BUSY_MESSAGE_OPTIONS}
            value={enterSendBehavior.value}
          />
        </PreferenceRow>
        <NotificationPreference />
      </div>
    </section>
  );
}

function NotificationPreference() {
  const [permission, setPermission] = useState<BrowserNotificationPermission>("unsupported");
  useEffect(() => setPermission(browserNotificationPermission()), []);

  if (permission === "unsupported") return null;
  const description =
    permission === "denied"
      ? "Blocked for this site; re-enable notifications in your browser settings."
      : "Get an alert when an off-screen agent run finishes.";

  return (
    <PreferenceRow description={description} label="Browser notifications">
      <div className="flex w-44 items-center gap-2">
        <Button
          className="min-h-11 flex-1"
          disabled={permission === "granted"}
          onClick={() => void requestBrowserNotifications().then(setPermission)}
          size="sm"
          variant="outline"
        >
          <Bell aria-hidden="true" />
          {permission === "granted" ? "Enabled" : "Enable"}
        </Button>
        <Button
          aria-label="Send a test notification"
          className="min-h-11 min-w-11"
          disabled={permission !== "granted"}
          onClick={() =>
            sendBrowserNotification("Mecatl notifications are ready", {
              body: "You’ll be notified when an off-screen run finishes.",
              icon: "/stacklok-favicon.png",
              tag: "mecatl-notification-test",
            })
          }
          size="icon"
          title="Send a test notification"
          variant="outline"
        >
          <BellRing aria-hidden="true" />
        </Button>
      </div>
    </PreferenceRow>
  );
}

function PreferenceRow({
  children,
  description,
  label,
}: {
  children: React.ReactNode;
  description: string;
  label: string;
}) {
  return (
    <div className="grid gap-3 py-4 first:pt-0 last:pb-0 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <div>
        <h3 className="text-sm font-medium">{label}</h3>
        <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
      </div>
      {children}
    </div>
  );
}
