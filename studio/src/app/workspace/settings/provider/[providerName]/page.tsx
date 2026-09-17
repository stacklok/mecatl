import { redirect } from "next/navigation";

/** Provider model lists are dedicated pages now; keep old links working. */
export default async function LegacyProviderModelsPage({
  params,
}: {
  params: Promise<{ providerName: string }>;
}) {
  const { providerName } = await params;
  redirect(`/workspace/provider/${providerName}`);
}
