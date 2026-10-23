---
matlatl: orphan-intentional
paths:
  - "apps/web/src/**"
---
# Studio web app

Routes are TanStack file routes in `apps/web/src/routes/`. A dot nests a route in its parent's layout (`workspace.chat.tsx` renders inside `workspace.tsx`), `$name` is a path parameter, and a trailing `_` on a segment leaves the parent's layout (`workspace.schedules_.$scheduleName.tsx`). A route file validates search and path parameters, redirects, and renders a component from `features/` or `components/shell/`. `routeTree.gen.ts` is [generated](../../apps/AGENTS.md#generated-files) from these files.

Each surface keeps its components and helpers in `features/<surface>/`. `components/shell/` holds the app frame, and `lib/` and `hooks/` hold shared helpers. Some features import each other today; #1777 owns the layout decision.

Request data only through the generated client (`@mecatl-studio/contracts/generated`, `/query`, and `/client`), configured once in `lib/api-client.ts` with the CSRF header and auth recovery. The only hand-built BFF URLs are browser navigations: the login URL in `features/auth/auth-gate.tsx` and the authorization presentation in `features/chat/authorization-review.tsx`.

Build controls from the shadcn/ui ports in `components/ui/`. When shadcn/ui provides a control that is missing there, port the primitive into `components/ui/` instead of hand-rolling it (#2201).

Layouts pivot at 500px, like the prototype: `min-[500px]:` and `max-[499px]:` in classes, and `useIsMobile()` from `lib/use-mobile.ts` in code. The chat list and side panels also pivot at 760px (`min-[760px]:`). `features/breakpoints.test.ts` fails on a new viewport `sm:` or `md:` class until the test lists it with a reason (it skips `features/schedules/` for now); `components/ui/` keeps its upstream shadcn classes, and container queries pass.

A visual change needs the screenshots described in [the Studio agent contract](../../apps/AGENTS.md#ui-pull-requests).
