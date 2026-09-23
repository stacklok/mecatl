// SPDX-License-Identifier: Apache-2.0

import { User } from "lucide-react";
import { Input } from "../../components/ui/input";
import { useUserAvatar, useUserDisplayName } from "../../lib/profile-preferences";
import { AvatarPicker } from "./avatar-picker";

export function IdentitySettings() {
  const userName = useUserDisplayName();
  const userAvatar = useUserAvatar();

  return (
    <IdentityCard
      description="Source: browser profile preferences. Owner: personal. These details appear beside your chat messages; your sign-in identity comes from the authenticated session and is read-only here."
      title="You"
    >
      <IdentityField description="What the agent should call you." label="Display name">
        <Input
          aria-label="Your display name"
          className="min-h-11 max-w-64"
          maxLength={40}
          onChange={(event) => userName.setValue(event.target.value)}
          placeholder="You"
          value={userName.value}
        />
      </IdentityField>
      <IdentityField description="Shown beside your messages." label="Picture">
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
    <section className="rounded-2xl border bg-card p-5 sm:p-6">
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="mt-1 text-sm text-muted-foreground">{description}</p>
      <div className="mt-5 divide-y">{children}</div>
    </section>
  );
}

export function IdentityField({
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
