// SPDX-License-Identifier: Apache-2.0

/** One tool call as the transcript sees it; `tool-call-list.tsx` renders it. */
export interface ToolActivity {
  args: string;
  id: string;
  isError?: boolean;
  name: string;
  output?: string;
  /** Present only for a tool.call observed on a specific live or replayed run. */
  runId?: string;
}
