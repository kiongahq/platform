import type { ReactNode } from "react";
import { StudioShell } from "@/components/studio/StudioShell";

export default function StudioLayout({ children }: { children: ReactNode }) {
  return <StudioShell>{children}</StudioShell>;
}
