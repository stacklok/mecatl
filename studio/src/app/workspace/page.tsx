import { redirect } from "next/navigation";

/** Token usage is the console's default landing page. */
export default function HarnessPage() {
  redirect("/workspace/chat");
}
