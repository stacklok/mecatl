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
  useShowStarterPrompts,
  useShowToolCalls,
  useStartOn,
  useUiScale,
} from "../../lib/profile-preferences";
import { type Theme, useTheme } from "../../lib/theme";
import { SettingsCard, SettingsRow } from "./settings-card";

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

/**
 * Settings → Personalise, in the prototype's three cards: Appearance, Chat,
 * and Notifications. Every preference here is personal to this browser.
 */
export function InterfaceSettings() {
  return (
    <>
      <AppearanceCard />
      <ChatCard />
      <NotificationsCard />
    </>
  );
}

function AppearanceCard() {
  const theme = useTheme();
  const palette = usePalette();
  const scale = useUiScale();
  const sessionListSide = useSessionListSide();

  return (
    <SettingsCard title="Appearance">
      <div className="divide-y divide-border/60">
        <SettingsRow description="Light, dark, or match your device." label="Theme">
          <OptionField
            label="Theme"
            onChange={(value) => theme.setTheme(value as Theme)}
            options={THEME_OPTIONS}
            value={theme.theme}
          />
        </SettingsRow>
        <SettingsRow description="Accent colors for buttons and highlights." label="Palette">
          <OptionField
            label="Palette"
            onChange={(value) => palette.setPalette(value as Palette)}
            options={PALETTE_OPTIONS}
            value={palette.palette}
          />
        </SettingsRow>
        <SettingsRow
          description="Makes text and controls bigger or smaller."
          label="Interface scale"
        >
          {/* Same footprint as the option triggers, so the control column lines up. */}
          <div className="flex w-44 items-center justify-between gap-1">
            <Button
              aria-label="Decrease interface scale"
              className="min-h-11 min-w-11 rounded-full"
              disabled={scale.value <= uiScaleMin}
              onClick={() => scale.setValue(scale.value - 0.05)}
              size="icon"
              variant="outline"
            >
              <Minus aria-hidden="true" className="size-4" />
            </Button>
            <span className="text-center text-sm tabular-nums">
              {Math.round(scale.value * 100)}%
            </span>
            <Button
              aria-label="Increase interface scale"
              className="min-h-11 min-w-11 rounded-full"
              disabled={scale.value >= uiScaleMax}
              onClick={() => scale.setValue(scale.value + 0.05)}
              size="icon"
              variant="outline"
            >
              <Plus aria-hidden="true" className="size-4" />
            </Button>
          </div>
        </SettingsRow>
        {/* Meaningless on a phone, where the chat list is full screen. */}
        <SettingsRow
          className="max-[499px]:hidden"
          description="Which side the chat list sits on."
          label="Sidebar side"
        >
          <OptionField
            label="Sidebar side"
            onChange={(value) => sessionListSide.setValue(value as SessionListSide)}
            options={SIDE_OPTIONS}
            value={sessionListSide.value}
          />
        </SettingsRow>
      </div>
    </SettingsCard>
  );
}

function ChatCard() {
  const startOn = useStartOn();
  const starterPrompts = useShowStarterPrompts();
  const enterSendBehavior = useEnterSendBehavior();
  const showToolCalls = useShowToolCalls();

  return (
    <SettingsCard title="Chat">
      <div className="divide-y divide-border/60">
        <SettingsRow description="What opens when you go to Chat." label="Start on">
          <OptionField
            label="Start on"
            onChange={(value) => startOn.setValue(value as StartOn)}
            options={START_ON_OPTIONS}
            value={startOn.value}
          />
        </SettingsRow>
        <SettingsRow description="Show suggested prompts on a new chat." label="Starter prompts">
          <label
            className="flex min-h-11 min-w-11 items-center justify-end"
            htmlFor="starter-prompts"
          >
            <Switch
              aria-label="Starter prompts"
              checked={starterPrompts.value}
              id="starter-prompts"
              onCheckedChange={(checked) => starterPrompts.setValue(checked === true)}
            />
          </label>
        </SettingsRow>
        <SettingsRow
          description="What Enter does while the agent is replying; Shift+Enter does the opposite."
          label="Messages while the agent works"
        >
          <OptionField
            label="Messages while the agent works"
            onChange={(value) => enterSendBehavior.setValue(value as EnterSendBehavior)}
            options={BUSY_MESSAGE_OPTIONS}
            value={enterSendBehavior.value}
          />
        </SettingsRow>
        <SettingsRow
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
        </SettingsRow>
      </div>
    </SettingsCard>
  );
}

function NotificationsCard() {
  const [permission, setPermission] = useState<BrowserNotificationPermission>("unsupported");
  useEffect(() => setPermission(browserNotificationPermission()), []);

  if (permission === "unsupported") return null;
  const description =
    permission === "denied"
      ? "Blocked for this site; re-enable notifications in your browser settings."
      : "Get an alert when an off-screen agent run finishes.";

  return (
    <SettingsCard title="Notifications">
      <SettingsRow description={description} label="Browser notifications">
        <div className="flex w-44 items-center gap-2">
          <Button
            className="min-h-11 flex-1 rounded-full"
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
            className="min-h-11 min-w-11 rounded-full"
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
      </SettingsRow>
    </SettingsCard>
  );
}
