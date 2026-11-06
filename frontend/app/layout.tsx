import type { ReactNode } from "react";
import localFont from "next/font/local";
import "@xyflow/react/dist/style.css";
import "./globals.css";

const body = localFont({ src: [
  { path: "../public/fonts/ibm-plex-sans-400.ttf", weight: "400" },
  { path: "../public/fonts/ibm-plex-sans-500.ttf", weight: "500" },
], variable: "--font-ibm-plex", display: "swap" });
const display = localFont({ src: [
  { path: "../public/fonts/familjen-grotesk-500.ttf", weight: "500" },
  { path: "../public/fonts/familjen-grotesk-600.ttf", weight: "600" },
], variable: "--font-familjen", display: "swap" });
const mono = localFont({ src: [
  { path: "../public/fonts/jetbrains-mono-400.ttf", weight: "400" },
  { path: "../public/fonts/jetbrains-mono-500.ttf", weight: "500" },
], variable: "--font-jetbrains", display: "swap" });

export const metadata = {
  title: { default: "Kionga", template: "%s | Kionga" },
  description: "The self-hosted control plane for ML, data, and AI.",
  icons: { icon: "/favicon.svg?v=ink-1" },
};
export default function RootLayout({ children }: { children: ReactNode }) {
  return <html lang="en" className={`${body.variable} ${display.variable} ${mono.variable}`} suppressHydrationWarning><body>{children}</body></html>;
}
