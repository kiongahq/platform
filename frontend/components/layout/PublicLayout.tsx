import type { ReactNode } from "react";
import Link from "next/link";
import { AsciiWordmark } from "@/components/landing/AsciiWordmark";
import { AsciiFlow } from "@/components/landing/AsciiFlow";
export function PublicLayout({
  children,
  brandFooter = false,
  matrix = false,
}: {
  children: ReactNode;
  brandFooter?: boolean;
  matrix?: boolean;
}) {
  return (
    <div className={`public-shell${matrix ? " kionga-matrix" : ""}`}>
      {matrix && <AsciiFlow />}
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="site-header">
        <Link className="wordmark" href="/">
          <img src="/logo.svg" alt="" width="87" height="24" />
          <strong>Kionga</strong>
        </Link>
        <nav aria-label="Site">
          <Link href="/">Platform</Link>
          <Link href="/api-docs">API reference</Link>
        </nav>
        <Link className="button primary" href="/console">
          Open console
        </Link>
      </header>
      <main id="main">{children}</main>
      <footer className={`site-footer${brandFooter ? " brand-footer" : ""}`}>
        {brandFooter && <AsciiWordmark />}
        <div className="footer-top">
          <Link className="wordmark" href="/">
            Kionga
          </Link>
          <p>ML, data and AI on infrastructure you control.</p>
          <nav aria-label="Footer">
            <Link href="/console">Console</Link>
            <a href="https://github.com/kiongahq">GitHub</a>
          </nav>
        </div>
      </footer>
    </div>
  );
}
