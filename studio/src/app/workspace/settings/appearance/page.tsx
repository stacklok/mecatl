"use client";

import {
  Bell,
  BellRing,
  CornerDownRight,
  ListEnd,
  Minus,
  Monitor,
  Moon,
  PanelLeft,
  PanelRight,
  Plus,
  Sun,
} from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  type EnterSendBehavior,
  UI_SCALE_MAX,
  UI_SCALE_MIN,
  useEnterSendBehavior,
  useSessionListSide,
  useUiScale,
} from "@/lib/profile-preferences";
import { OptionField } from "../_components/option-field";
import { SettingsCard, SettingsRow } from "../_components/settings-card";

const THEME_OPTIONS = [
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
  { value: "system", label: "System", icon: Monitor },
] as const;

const SIDE_OPTIONS = [
  { value: "left", label: "Left", icon: PanelLeft },
  { value: "right", label: "Right", icon: PanelRight },
] as const;

const ENTER_BEHAVIOR_OPTIONS = [
  { value: "queue", label: "Queue message", icon: ListEnd },
  { value: "steer", label: "Steer the agent", icon: CornerDownRight },
] as const;

export default function AppearanceSettingsPage() {
  const { theme: activeTheme, setTheme } = useTheme();
  const { side, setSide } = useSessionListSide();
  const { scale, setScale } = useUiScale();
  const { behavior, setBehavior } = useEnterSendBehavior();

  // Browser notifications: permission mirrored into state so the row reflects
  // granted / denied / not-yet-asked; "unsupported" hides the row's actions.
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
      toast.error("Notifications are blocked — enable them in your browser.");
    }
  }

  function sendTestNotification() {
    if (
      typeof Notification === "undefined" ||
      Notification.permission !== "granted"
    ) {
      return;
    }
    new Notification("Scheduled task finished", {
      body: "Daily dependency audit completed — 0 critical vulnerabilities found.",
      tag: "atrium-example",
      icon: "/favicon.ico",
    });
    toast.success("Test notification sent");
  }

  // next-themes resolves only on the client; gate the current value on mount
  // so the trigger shows the real choice instead of a flash of "system".
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  return (
    <SettingsCard title="Personalize">
      <div className="divide-y divide-border/60">
        <SettingsRow
          label="Theme"
          description="Light, dark, or follow the system."
        >
          <OptionField
            label="Theme"
            value={mounted ? (activeTheme ?? "system") : "system"}
            options={THEME_OPTIONS}
            onChange={setTheme}
          />
        </SettingsRow>

        <SettingsRow
          label="Interface scale"
          description="Sizes text and controls together."
        >
          {/* Same footprint as the OptionField triggers so the control
              column lines up. */}
          <div className="flex w-44 items-center justify-between gap-1">
            <Button
              variant="outline"
              size="icon"
              className="size-8 rounded-full"
              aria-label="Decrease interface scale"
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
              aria-label="Increase interface scale"
              disabled={scale >= UI_SCALE_MAX}
              onClick={() => setScale(scale + 0.05)}
            >
              <Plus className="size-4" />
            </Button>
          </div>
        </SettingsRow>

        {/* Meaningless on mobile — the session list is full-screen there. */}
        <SettingsRow
          label="Session list position"
          description="Which side of the window the session list docks on."
          className="max-[499px]:hidden"
        >
          <OptionField
            label="Session list position"
            value={side}
            options={SIDE_OPTIONS}
            onChange={(next) => setSide(next as "left" | "right")}
          />
        </SettingsRow>

        <SettingsRow
          label="Message queuing"
          description="Shift+Enter does the opposite."
        >
          <OptionField
            label="Message queuing"
            value={behavior}
            options={ENTER_BEHAVIOR_OPTIONS}
            onChange={(next) => setBehavior(next as EnterSendBehavior)}
          />
        </SettingsRow>

        {notifyPermission !== "unsupported" && (
          <SettingsRow
            label="Browser notifications"
            description={
              notifyPermission === "denied"
                ? "Blocked — re-enable them for this site in your browser settings."
                : "Get a browser alert when a run or scheduled task finishes."
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
        )}
      </div>
    </SettingsCard>
  );
}
