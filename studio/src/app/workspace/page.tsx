import { redirect } from "next/navigation";

/**
 * The workspace lands on the chat draft route. Whether that route then shows
 * a new draft or opens the most recent chat (the `--resume-latest` analogue)
 * is the browser-local "Start on" preference under Settings → Personalize,
 * applied by the chat workspace itself once the daemon is connected.
 */
export default function HarnessPage() {
  redirect("/workspace/chat");
}
