"use client";
import { publicationHref } from "@/lib/publication";
import { SessionNotice } from "@/components/layout/SessionNotice";
import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  editorial,
  EditorialError,
  roles,
  type Session,
} from "@/lib/editorial";
export const StudioContext = createContext<{
  session: Session;
  notify: (message: string) => void;
  setGuard: (guard: (() => boolean | Promise<boolean>) | null) => void;
} | null>(null);
export function useStudio() {
  const value = useContext(StudioContext);
  if (!value) throw new Error("Studio context is unavailable");
  return value;
}
export type StudioView =
  | "posts"
  | "drafts"
  | "reviews"
  | "media"
  | "authors"
  | "settings"
  | "new"
  | "edit";
export function StudioShell({
  view,
  children,
}: {
  view: StudioView;
  children: React.ReactNode;
}) {
  const [session, setSession] = useState<Session | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [denied, setDenied] = useState<Record<string, any> | null>(null),
    [name, setName] = useState(""),
    [notice, setNotice] = useState("");
  const guard = useRef<(() => boolean | Promise<boolean>) | null>(null);
  const router = useRouter();
  const setGuard = useCallback(
    (next: (() => boolean | Promise<boolean>) | null) => {
      guard.current = next;
    },
    [],
  );
  const notify = useCallback((message: string) => setNotice(message), []);
  const opening = useRef<Promise<Session> | null>(null);
  const start = useCallback(async () => {
    setLoading(true);
    setError("");
    if (!opening.current)
      opening.current = (async () => {
        try {
          return await editorial<Session>("/api/v1/editorial/session");
        } catch (failure) {
          if (!(failure instanceof EditorialError) || failure.status !== 403)
            throw failure;
          await editorial("/api/v1/editorial/session", { method: "POST" });
          return editorial<Session>("/api/v1/editorial/session");
        }
      })();
    const pending = opening.current;
    try {
      setSession(await pending);
      setDenied(null);
    } catch (failure) {
      if (failure instanceof EditorialError && failure.status === 403)
        setDenied(failure.body);
      else setError((failure as Error).message);
    } finally {
      if (opening.current === pending) opening.current = null;
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void start();
  }, [start]);
  async function navigate(
    event: React.MouseEvent<HTMLAnchorElement>,
    href: string,
  ) {
    if (
      !guard.current ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    )
      return;
    event.preventDefault();
    if (await guard.current()) router.push(href);
  }
  const links: StudioView[] = [
    "posts",
    "drafts",
    "reviews",
    "media",
    "authors",
    "settings",
  ];
  return (
    <div className="studio-shell app-shell">
      <a href="#ed-main" className="skip-link">
        Skip to content
      </a>
      <header className="app-bar studio-header">
        <Link
          href="/studio"
          className="app-brand"
          onClick={(e) => void navigate(e, "/studio")}
        >
          <img src="/logo.svg" alt="" width="87" height="24" />
          Kionga Editorial
        </Link>
        <nav aria-label="Publication">
          <a
            href={publicationHref()}
            onClick={async (e) => {
              if (
                guard.current &&
                !e.metaKey &&
                !e.ctrlKey &&
                !e.shiftKey &&
                !e.altKey
              ) {
                e.preventDefault();
                if (await guard.current()) location.assign(publicationHref());
              }
            }}
          >
            View publication
          </a>
          <Link href="/console" onClick={(e) => void navigate(e, "/console")}>
            ML console
          </Link>
        </nav>
        {session && (
          <div id="ed-member">
            <strong id="ed-member-name">{session.member.display_name}</strong>
            <span id="ed-member-role">{roles[session.member.role]}</span>
            <button
              id="ed-signout"
              type="button"
              onClick={async () => {
                if (guard.current && !(await guard.current())) return;
                try {
                  await editorial("/api/v1/editorial/session", {
                    method: "DELETE",
                  });
                  location.assign(publicationHref());
                } catch (failure) {
                  setError((failure as Error).message);
                }
              }}
            >
              Leave workspace
            </button>
          </div>
        )}
      </header>
      {session && (
        <aside className="app-sidebar">
          <nav id="ed-nav" aria-label="Editorial navigation">
            {links.map((item) => (
              <Link
                key={item}
                href={`/studio/${item}`}
                aria-current={view === item ? "page" : undefined}
                onClick={(e) => void navigate(e, `/studio/${item}`)}
              >
                {item[0].toUpperCase() + item.slice(1)}
              </Link>
            ))}
          </nav>
        </aside>
      )}
      <main id="ed-main" className="studio-main app-content" tabIndex={-1}>
        <SessionNotice />
        {loading ? (
          <p role="status">Opening the editorial workspace…</p>
        ) : denied ? (
          <section className="panel">
            <h1 id="ed-denied">You don&apos;t have editorial access</h1>
            <p>
              Writing and publishing requires an editorial membership. Ask an
              editorial administrator to add your account.
            </p>
            {denied.bootstrap_available && denied.can_bootstrap && (
              <div>
                <p>
                  No editorial administrator exists yet. A platform
                  administrator can claim the first membership; this is recorded
                  in the audit log.
                </p>
                <label>
                  Your byline
                  <input
                    id="ed-bootstrap-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                  />
                </label>
                <button
                  className="primary"
                  id="ed-bootstrap"
                  onClick={async () => {
                    try {
                      await editorial("/api/v1/editorial/bootstrap", {
                        method: "POST",
                        body: { display_name: name },
                      });
                      await start();
                    } catch (failure) {
                      setError((failure as Error).message);
                    }
                  }}
                >
                  Become the first editorial admin
                </button>
              </div>
            )}
          </section>
        ) : session ? (
          <StudioContext.Provider value={{ session, notify, setGuard }}>
            {children}
          </StudioContext.Provider>
        ) : null}
        {error && (
          <div className="feedback error" role="alert">
            {error}
            <button onClick={() => void start()}>Retry</button>
          </div>
        )}
        {notice && (
          <div id="ed-toast" className="feedback" role="status">
            {notice}
            <button
              aria-label="Dismiss notification"
              onClick={() => setNotice("")}
            >
              Dismiss
            </button>
          </div>
        )}
      </main>
    </div>
  );
}
