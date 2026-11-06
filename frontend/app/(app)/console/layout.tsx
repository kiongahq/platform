import type { ReactNode } from "react";
import ConsolePage from "@/components/pages/ConsolePage";

// Keep session data, navigation, preferences and event subscriptions mounted
// while the selected screen changes through the App Router.
export default function ConsoleLayout({ children }: { children: ReactNode }) {
  return <><ConsolePage />{children}</>;
}
