"use client";

import {
  Bell,
  BellRing,
  Clock,
  History,
  Hourglass,
  Minus,
  Monitor,
  Moon,
  PanelLeft,
  PanelRight,
  Plus,
  SquarePen,
  Sun,
  Zap,
} from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { usePalette } from "@/components/palette-provider";
import { paletteSwatch } from "@/components/palette-swatch";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import type { PaletteDef } from "@/lib/palettes";
import {
  type EnterSendBehavior,
  type LaunchTarget,
  UI_SCALE_MAX,
  UI_SCALE_MIN,
  useEnterSendBehavior,
  useLaunchTarget,
  useSessionListSide,
  useShowStarterPrompts,
  useUiScale,
} from "@/lib/profile-preferences";
import { OptionField } from "../_components/option-field";
import { SettingsCard, SettingsRow } from "../_components/settings-card";

const THEME_OPTIONS = [
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
  { value: "system", label: "System", icon: Monitor },
] as const;

// One option per palette, built-in or custom, with a swatch in the palette's
// accent. Memoised on the catalogue so the swatch component identities stay
// stable across renders.
function paletteOptions(catalogue: readonly PaletteDef[]) {
  return catalogue.map((palette) => ({
    value: palette.id,
    label:
      palette.source === "built-in"
        ? palette.label
        : `${palette.label} · custom`,
    description: palette.description,
    icon: paletteSwatch(palette.swatch),
  }));
}

const SIDE_OPTIONS = [
  { value: "left", label: "Left", icon: PanelLeft },
  { value: "right", label: "Right", icon: PanelRight },
] as const;

const LAUNCH_TARGET_OPTIONS = [
  { value: "draft", label: "New chat", icon: SquarePen },
  { value: "latest", label: "Most recent chat", icon: History },
] as const;

// The three stored values are unchanged: "queue" (Enter waits, Shift+Enter
// interrupts), "steer" (the reverse) and "queue-only" (both wait). Only the
// words are plainer.
const BUSY_MESSAGE_OPTIONS = [
  { value: "queue", label: "Wait for the agent", icon: Clock },
  { value: "steer", label: "Interrupt the agent", icon: Zap },
  {
    value: "queue-only",
    label: "Always wait",
    icon: Hourglass,
    description: "Never interrupt, even with Shift+Enter.",
  },
] as const;

/** The row's description, phrased for the chosen value. */
function busyMessageDescription(behavior: EnterSendBehavior): string {
  switch (behavior) {
    case "steer":
      return "A message you send now interrupts the agent; Shift+Enter makes it wait instead.";
    case "queue-only":
      return "Every message waits until the agent finishes, even with Shift+Enter.";
    default:
      return "A message you send now waits until the agent finishes; Shift+Enter interrupts instead.";
  }
}

function AppearanceCard() {
  const { theme: activeTheme, setTheme } = useTheme();
  const { palette, setPalette, catalogue } = usePalette();
  const paletteOptionList = useMemo(
    () => paletteOptions(catalogue),
    [catalogue],
  );
  const { side, setSide } = useSessionListSide();
  const { scale, setScale } = useUiScale();

  // next-themes resolves only on the client; gate the current value on mount
  // so the trigger shows the real choice instead of a flash of "system".
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  return (
    <SettingsCard
      title="Appearance"
      description="How Studio looks in this browser."
    >
      <div className="divide-y divide-border/60">
        <SettingsRow
          label="Theme"
          description="Light, dark, or match your device."
        >
          <OptionField
            label="Theme"
            value={mounted ? (activeTheme ?? "system") : "system"}
            options={THEME_OPTIONS}
            onChange={setTheme}
          />
        </SettingsRow>

        <SettingsRow
          label="Palette"
          description="Accent colours for buttons and highlights."
        >
          <OptionField
            label="Palette"
            value={palette}
            options={paletteOptionList}
            onChange={setPalette}
          />
        </SettingsRow>

        <SettingsRow
          label="Text size"
          description="Makes text and controls bigger or smaller."
        >
          {/* Same footprint as the OptionField triggers so the control
              column lines up. */}
          <div className="flex w-44 items-center justify-between gap-1">
            <Button
              variant="outline"
              size="icon"
              className="size-8 rounded-full"
              aria-label="Decrease text size"
              disabled={scale <= UI_SCALE_MIN}
              onClick={() => setScale(scale - 0.05)}
            >
              <Minus className="size-4" />
            </Button>
            <span className="text-center text-sm tabular-nums">
              {Math.round(scale * 100)}%
            </span>
            <Button
              variant="outline"
              size="icon"
              className="size-8 rounded-full"
              aria-label="Increase text size"
              disabled={scale >= UI_SCALE_MAX}
              onClick={() => setScale(scale + 0.05)}
            >
              <Plus className="size-4" />
            </Button>
          </div>
        </SettingsRow>

        {/* Meaningless on mobile — the chat list is full-screen there. */}
        <SettingsRow
          label="Sidebar side"
          description="Which side the chat list sits on."
          className="max-[499px]:hidden"
        >
          <OptionField
            label="Sidebar side"
            value={side}
            options={SIDE_OPTIONS}
            onChange={(next) => setSide(next as "left" | "right")}
          />
        </SettingsRow>
      </div>
    </SettingsCard>
  );
}

function ChatCard() {
  const { behavior, setBehavior } = useEnterSendBehavior();
  const { target: launchTarget, setTarget: setLaunchTarget } =
    useLaunchTarget();
  const { show: showStarterPrompts, setShow: setShowStarterPrompts } =
    useShowStarterPrompts();

  return (
    <SettingsCard
      title="Chat"
      description="How chats open and how your messages are sent."
    >
      <div className="divide-y divide-border/60">
        <SettingsRow
          label="Start on"
          description="What opens when you go to Chat."
        >
          <OptionField
            label="Start on"
            value={launchTarget}
            options={LAUNCH_TARGET_OPTIONS}
            onChange={(next) => setLaunchTarget(next as LaunchTarget)}
          />
        </SettingsRow>

        <SettingsRow
          label="Starter prompts"
          htmlFor="starter-prompts"
          description="Show suggested prompts on a new chat."
        >
          <Switch
            id="starter-prompts"
            checked={showStarterPrompts}
            onCheckedChange={setShowStarterPrompts}
            aria-label="Starter prompts"
          />
        </SettingsRow>

        <SettingsRow
          label="Messages while the agent works"
          description={busyMessageDescription(behavior)}
        >
          <OptionField
            label="Messages while the agent works"
            value={behavior}
            options={BUSY_MESSAGE_OPTIONS}
            onChange={(next) => setBehavior(next as EnterSendBehavior)}
          />
        </SettingsRow>
      </div>
    </SettingsCard>
  );
}

function NotificationsCard() {
  // Browser notifications: permission mirrored into state so the row reflects
  // granted / denied / not-yet-asked; "unsupported" hides the whole card.
  const [notifyPermission, setNotifyPermission] = useState<
    NotificationPermission | "unsupported"
  >("default");
  useEffect(() => {
    if (typeof window !== "undefined" && "Notification" in window) {
      setNotifyPermission(Notification.permission);
    } else {
      setNotifyPermission("unsupported");
    }
  }, []);

  async function enableNotifications() {
    if (typeof Notification === "undefined") return;
    const result = await Notification.requestPermission();
    setNotifyPermission(result);
    if (result === "granted") {
      toast.success("Browser notifications enabled");
    } else if (result === "denied") {
      toast.error(
        "Notifications are blocked — allow them for this site in your browser.",
      );
    }
  }

  function sendTestNotification() {
    if (
      typeof Notification === "undefined" ||
      Notification.permission !== "granted"
    ) {
      return;
    }
    new Notification("Test notification", {
      body: "This is how Studio will alert you when the agent finishes a task.",
      tag: "atrium-example",
      icon: "/favicon.ico",
    });
    toast.success("Test notification sent");
  }

  if (notifyPermission === "unsupported") return null;

  return (
    <SettingsCard
      title="Notifications"
      description="Alerts from this browser when the agent finishes."
    >
      <SettingsRow
        label="Browser notifications"
        description={
          notifyPermission === "denied"
            ? "Blocked in your browser — allow notifications for this site to turn them on."
            : "Get an alert when the agent finishes a task."
        }
      >
        <div className="flex w-44 items-center gap-2">
          <Button
            variant="outline"
            className="flex-1 rounded-full"
            onClick={enableNotifications}
            disabled={notifyPermission === "granted"}
          >
            <Bell className="size-4" />
            {notifyPermission === "granted" ? "Enabled" : "Enable"}
          </Button>
          <Button
            variant="outline"
            size="icon"
            className="size-9 shrink-0 rounded-full"
            aria-label="Send a test notification"
            title="Send a test notification"
            onClick={sendTestNotification}
            disabled={notifyPermission !== "granted"}
          >
            <BellRing className="size-4" />
          </Button>
        </div>
      </SettingsRow>
    </SettingsCard>
  );
}

export default function AppearanceSettingsPage() {
  return (
    <>
      <AppearanceCard />
      <ChatCard />
      <NotificationsCard />
    </>
  );
}
