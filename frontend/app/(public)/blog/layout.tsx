import type { ReactNode } from "react";
import { PublicationLayout } from "@/components/layout/PublicationLayout";
export default function BlogLayout({ children }: { children: ReactNode }) {
  return <PublicationLayout>{children}</PublicationLayout>;
}
