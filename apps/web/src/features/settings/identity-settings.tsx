// SPDX-License-Identifier: Apache-2.0

import { User } from "lucide-react";
import { Input } from "../../components/ui/input";
import { useUserAvatar, useUserDisplayName } from "../../lib/profile-preferences";
import { AvatarPicker } from "./avatar-picker";
import { Note, SettingsCard, SettingsRow } from "./settings-card";

export function IdentitySettings() {
  const userName = useUserDisplayName();
  const userAvatar = useUserAvatar();

  return (
    <IdentityCard
      description="These details appear beside your chat messages; your sign-in identity comes from the authenticated session and is read-only here."
      title="You"
    >
      <IdentityField
        description="Shown on your messages."
        htmlFor="user-display-name"
        label="Your name"
      >
        <Input
          aria-label="Your display name"
          className="min-h-11 w-44 min-[500px]:w-60"
          id="user-display-name"
          maxLength={40}
          onChange={(event) => userName.setValue(event.target.value)}
          placeholder="You"
          value={userName.value}
        />
      </IdentityField>
      <IdentityField description="Shown next to your messages." label="Picture">
        <AvatarPicker
          alt={userName.value.trim() || "You"}
          avatarUrl={userAvatar.value}
          fallback={<User aria-hidden="true" className="size-6" />}
          onChange={userAvatar.setValue}
        />
      </IdentityField>
    </IdentityCard>
  );
}

export function IdentityCard({
  children,
  description,
  title,
}: {
  children: React.ReactNode;
  description: string;
  title: string;
}) {
  return (
    <SettingsCard title={title}>
      <Note>{description}</Note>
      <div className="mt-5 divide-y divide-border/60">{children}</div>
    </SettingsCard>
  );
}

export function IdentityField({
  children,
  description,
  htmlFor,
  label,
}: {
  children: React.ReactNode;
  description: string;
  htmlFor?: string;
  label: string;
}) {
  return (
    <SettingsRow description={description} htmlFor={htmlFor} label={label}>
      {children}
    </SettingsRow>
  );
}
