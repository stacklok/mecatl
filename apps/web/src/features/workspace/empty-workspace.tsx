// SPDX-License-Identifier: Apache-2.0

/**
 * The bootstrap workspace: intentionally empty. Feature plans replace this
 * with the chat workspace and its siblings.
 */
export function EmptyWorkspace() {
  return (
    <section className="flex h-full items-center justify-center p-8 text-center">
      <div className="max-w-md space-y-2">
        <h1 className="text-lg font-semibold">Mecatl Studio</h1>
        <p className="text-sm text-muted-foreground">
          This deployment is running the Studio bootstrap. Features arrive in later releases.
        </p>
      </div>
    </section>
  );
}
