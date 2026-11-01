import type { ReactNode } from "react";
import Link from "next/link";

/** The adopter's publication has its own reader navigation and editorial identity. */
export function PublicationLayout({ children }: { children: ReactNode }) {
  return (
    <div className="public-shell publication-shell">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="site-header">
        <Link className="wordmark" href="/blog">
          Engineering
        </Link>
        <nav aria-label="Publication">
          <Link href="/blog">Articles</Link>
        </nav>
      </header>
      <main id="main">{children}</main>
      <footer className="site-footer">
        <Link href="/blog">All articles</Link>
        <span>Published with Kionga</span>
      </footer>
    </div>
  );
}
