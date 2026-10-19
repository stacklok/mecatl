// SPDX-License-Identifier: Apache-2.0

// Test-only helpers for the schedule workspace and detail tests: a fake BFF
// and both routes mounted in a memory router. Not imported by app code.

import { client as apiClient } from "@mecatl-studio/contracts/client";
import type {
  ListScheduleFiresResponse,
  ListSchedulesResponse,
} from "@mecatl-studio/contracts/generated";
import { listSchedulesQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { render } from "@testing-library/react";
import { Toaster } from "../../components/ui/sonner";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { ScheduleDetail } from "./schedule-detail";
import { SchedulesWorkspace } from "./schedules-workspace";

export type Schedule = ListSchedulesResponse["items"][number];
export type Fire = ListScheduleFiresResponse["items"][number];

export function schedule(overrides: Partial<Schedule> & Pick<Schedule, "name">): Schedule {
  return {
    enabled: true,
    fireCount: 0,
    lastFireAt: "",
    lastFireSessionId: "",
    maxFires: 0,
    mode: "default",
    modelId: "",
    mutating: false,
    nextFireAt: "",
    oneShotMaxRetries: 0,
    oneShotRetry: false,
    owner: "",
    profile: "all",
    prompt: "prompt",
    providerId: "",
    status: "scheduled",
    trigger: { expression: "0 9 * * *", kind: "cron", timezone: "" },
    ...overrides,
  };
}

export function fire(overrides: Partial<Fire> & Pick<Fire, "id">): Fire {
  return {
    deadline: "",
    error: "",
    firedAt: "2026-10-07T09:00:00Z",
    inFlight: false,
    outcome: "completed",
    progressAt: "",
    scheduleName: "Alpha",
    sessionId: "",
    startedAt: "2026-10-07T09:00:00Z",
    stop: "end_turn",
    ...overrides,
  };
}

export function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    headers: { "content-type": "application/json" },
    status,
  });
}

export function problem(detail: string, status = 500) {
  return new Response(
    JSON.stringify({ code: "", detail, instance: "", status, title: "", type: "" }),
    { headers: { "content-type": "application/problem+json" }, status },
  );
}

export interface FakeRequest {
  body: unknown;
  method: string;
  path: string;
}

/**
 * A route the test answers itself; `undefined` falls through to the defaults:
 * the schedule list, one schedule's fires, and 204 for any write.
 */
export type FakeRoute = (request: FakeRequest) => Response | Promise<Response> | undefined;

const initialApiConfig = apiClient.getConfig();

export function resetApi() {
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
}

export function fakeApi(
  items: Schedule[],
  fires: Fire[] | "pending" = [],
  route: FakeRoute = () => undefined,
) {
  const requests: FakeRequest[] = [];
  apiClient.setConfig({
    baseUrl: window.location.origin,
    fetch: async (input, init) => {
      const request = new Request(input, init);
      const text = request.method === "GET" ? "" : await request.text();
      const fake = {
        body: text ? JSON.parse(text) : undefined,
        method: request.method,
        path: new URL(request.url).pathname,
      };
      requests.push(fake);
      const answer = route(fake);
      if (answer) return answer;
      if (fake.method !== "GET") return new Response(null, { status: 204 });
      if (fake.path.endsWith("/fires")) {
        return fires === "pending"
          ? new Promise<Response>(() => {})
          : jsonResponse({ items: fires });
      }
      return jsonResponse({ items, reason: "", supported: true });
    },
  });
  return requests;
}

/** Mounts the list and detail routes, starting on `path`, with the app's Toaster. */
export function mountSchedules(
  items: Schedule[],
  path: string,
  fires: Fire[] | "pending" = [],
  route?: FakeRoute,
) {
  const requests = fakeApi(items, fires, route);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  queryClient.setQueryData<ListSchedulesResponse>(listSchedulesQueryKey(), {
    items,
    reason: "",
    supported: true,
  });
  const root = createRootRoute();
  const list = createRoute({
    component: SchedulesWorkspace,
    getParentRoute: () => root,
    path: "/workspace/schedules",
  });
  const detail = createRoute({
    component: function Detail() {
      const { scheduleName } = detail.useParams();
      return <ScheduleDetail scheduleName={scheduleName} />;
    },
    getParentRoute: () => root,
    path: "/workspace/schedules/$scheduleName",
  });
  const chat = createRoute({
    component: () => <p>chat</p>,
    getParentRoute: () => root,
    path: "/workspace/chat",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree: root.addChildren([list, detail, chat]),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <ShortcutProvider>
        <RouterProvider router={router} />
        <Toaster />
      </ShortcutProvider>
    </QueryClientProvider>,
  );
  return { queryClient, requests, router };
}
