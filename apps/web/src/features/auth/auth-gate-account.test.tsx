// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { getAuthSessionOptions, listSessionsQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { AuthGate } from "./auth-gate";

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
});

function AccountData() {
  return (
    <span data-testid="account-data">
      {window.localStorage.getItem("studio.chat.folders") ?? "cleared"}
    </span>
  );
}

it("drops account data and prior BFF snapshots before the next account renders", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const authKey = getAuthSessionOptions().queryKey;
  client.setQueryData(authKey, { account: "alice", mode: "oidc", status: "authenticated" });
  client.setQueryData(listSessionsQueryKey(), {
    complete: true,
    items: [{ id: "same", title: "Alice" }],
  });
  window.localStorage.setItem("studio.account", "alice");
  window.sessionStorage.setItem("studio.account", "alice");
  window.localStorage.setItem("studio.chat.folders", "Alice's folder");
  window.sessionStorage.setItem("studio.chat.failed.same", "Alice's failed prompt");
  window.localStorage.setItem("theme", "dark");

  render(
    <QueryClientProvider client={client}>
      <AuthGate>
        <AccountData />
      </AuthGate>
    </QueryClientProvider>,
  );
  expect(screen.getByTestId("account-data").textContent).toBe("Alice's folder");

  client.setQueryData(authKey, { account: "bob", mode: "oidc", status: "authenticated" });
  await waitFor(() => expect(screen.getByTestId("account-data").textContent).toBe("cleared"));
  expect(window.sessionStorage.getItem("studio.chat.failed.same")).toBeNull();
  expect(client.getQueryData(listSessionsQueryKey())).toBeUndefined();
  expect(window.localStorage.getItem("theme")).toBe("dark");
  client.clear();
});
