import { redirect } from "next/navigation";

/**
 * Memory details are a dedicated page outside the settings nav now; keep the
 * old nested URL working for bookmarks.
 */
export default async function LegacySettingsMemoryDetailPage({
  params,
}: {
  params: Promise<{ memoryId: string }>;
}) {
  const { memoryId } = await params;
  redirect(`/workspace/memory/${memoryId}`);
}
