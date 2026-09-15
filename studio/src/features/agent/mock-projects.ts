/**
 * The Labs "mock features" Projects section: a static, clearly-labeled
 * demonstration of project-grouped chats in the sidebar. Everything in this
 * module is browser-local demo content — the ids must NEVER reach the daemon
 * (the rows are presentational; nothing here mints a session), and the
 * section renders only while the Labs toggle is on. Like the feature tour
 * (mock-tour.ts) this rides the one sanctioned mock-content exception to the
 * daemon-only rule (CLAUDE.md rule 1): explicit, default-off, and labeled —
 * never a fallback for an unreachable daemon.
 */

export interface MockProjectChat {
  id: string;
  title: string;
  /** Renders the green unread dot instead of the age label. */
  unread: boolean;
  /** Epoch ms; rendered via formatRelativeTime ("9h", "3d"). */
  updatedAt: number;
}

export interface MockProject {
  id: string;
  name: string;
  chats: MockProjectChat[];
}

// Stable within a page load; the section only renders client-side (the Labs
// preference hydrates after mount), so no SSR/hydration mismatch.
const NOW = Date.now();
const hoursAgo = (h: number) => NOW - h * 3_600_000;

export const MOCK_PROJECTS: MockProject[] = [
  {
    id: "mock-project-platform-engineering",
    name: "Platform Engineering",
    chats: [
      {
        id: "mock-project-chat-auth-middleware",
        title: "Refactor auth middleware",
        unread: false,
        updatedAt: hoursAgo(2),
      },
      {
        id: "mock-project-chat-ci-pipeline",
        title: "Debug CI pipeline failures",
        unread: true,
        updatedAt: hoursAgo(9),
      },
      {
        id: "mock-project-chat-pr-847",
        title: "Review PR #847 — rate limiter",
        unread: false,
        updatedAt: hoursAgo(3 * 24),
      },
    ],
  },
  {
    id: "mock-project-notifications-service",
    name: "Notifications Service",
    chats: [
      {
        id: "mock-project-chat-webhook-retries",
        title: "Webhook retry backoff tuning",
        unread: true,
        updatedAt: hoursAgo(5),
      },
      {
        id: "mock-project-chat-digest-emails",
        title: "Design the daily digest email",
        unread: false,
        updatedAt: hoursAgo(26),
      },
    ],
  },
  {
    id: "mock-project-q2-security-audit",
    name: "Q2 Security Audit",
    chats: [
      {
        id: "mock-project-chat-cve-triage",
        title: "Dependency CVE triage",
        unread: false,
        updatedAt: hoursAgo(6),
      },
      {
        id: "mock-project-chat-key-rotation",
        title: "Signing key rotation runbook",
        unread: false,
        updatedAt: hoursAgo(2 * 24),
      },
    ],
  },
];

/** The project expanded on first render, so the section demonstrates the
 *  open state (accent bar, open folder, indented chats) without a click. */
export const MOCK_PROJECTS_DEFAULT_OPEN_ID = MOCK_PROJECTS[0].id;
