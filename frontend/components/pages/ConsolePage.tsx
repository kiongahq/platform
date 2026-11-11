"use client";
import { createPortal } from "react-dom";
import { readConsolePreferences } from "@/lib/preferences";
import { Select } from "@/components/ui/Select";
import { SessionNotice } from "@/components/layout/SessionNotice";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import {
  Suspense,
  useCallback,
  useRef,
  useEffect,
  useState,
  type CSSProperties,
} from "react";
import { useAutoCollapse } from "@/components/layout/useAutoCollapse";
import { useQuery } from "@/hooks/useQuery";
import { UnsavedContext } from "@/hooks/useUnsaved";
import {
  hasService,
  isAdministrator,
  items,
  type Identity,
  type Entity,
} from "@/lib/types";
import { DataState } from "@/features/shared/View";
import { Icon } from "@/components/ui/Icon";
import { ResourceSearch } from "@/components/console/ResourceSearch";
import { OperationsView } from "@/features/operations/OperationsView";
import { LangflowView } from "@/features/langflow/LangflowView";
import { AgentsView } from "@/features/agents/AgentsView";
import { AgentDesigner } from "@/features/designer/AgentDesigner";
import { AgentDefinitionsView } from "@/features/teams/AgentDefinitionsView";
import { TeamsView } from "@/features/teams/TeamsView";
import { TeamRunsView } from "@/features/teams/TeamRunsView";
import { PipelineEditor } from "@/features/pipelines/PipelineEditor";
const groups = [
  {
    label: "Build",
    views: [
      ["overview", "Overview"],
      ["projects", "Projects"],
      ["pipelines", "Pipelines"],
      ["functions", "Functions"],
    ],
  },
  {
    label: "Models and data",
    views: [
      ["models", "Models"],
      ["agents", "Agents"],
      ["agent-designer", "Agent designer"],
      ["agent-definitions", "Agent definitions"],
      ["agent-teams", "Agent teams"],
      ["team-runs", "Team runs"],
      ["langflow", "Langflow"],
      ["features", "Feature stores"],
      ["storage", "Storage"],
      ["realtime", "Realtime"],
      ["catalog", "Catalog"],
    ],
  },
  {
    label: "Operate",
    views: [
      ["logs", "Logs"],
      ["platform", "Platform"],
      ["access", "Access"],
      ["blogs", "Publishing"],
      ["profile", "My access"],
      ["settings", "Settings"],
    ],
  },
];
const titles: Record<string, string> = Object.fromEntries(
  groups.flatMap((group) => group.views),
);
const service = (view: string) =>
  ({
    "agent-designer": "agents",
    "agent-definitions": "agents",
    "agent-teams": "agents",
    "team-runs": "agents",
    "pipeline-editor": "pipelines",
    logs: "pipelines",
  })[view] || view;
const ungated = new Set(["profile", "settings", "blogs"]);
export default function ConsolePage({ view }: { view?: string }) {
  return (
    <Suspense fallback={<p role="status">Opening console…</p>}>
      <ConsoleApplication view={view} />
    </Suspense>
  );
}
function ConsoleApplication({ view: requestedView }: { view?: string }) {
  const route = useRouter(),
    pathname = usePathname(),
    query = useSearchParams(),
    me = useQuery<Identity>("/api/v1/me"),
    projects = useQuery<Entity>("/api/v1/project-options"),
    workspaceQuery = useQuery<Entity>("/api/v1/workspaces");
  const view = requestedView ?? (pathname.split("/")[2] || "overview");
  const identity: Identity = me.data
    ? {
        ...me.data,
        roles: me.data.roles || [],
        services: me.data.services || [],
        permissions: me.data.permissions || {},
      }
    : { subject: "", roles: [], services: [], permissions: {}, mode: "" };
  const [navOpen, setNavOpen] = useState(false),
    [dirty, setDirty] = useState(false),
    [live, setLive] = useState("Connecting"),
    [prefs, setPrefs] = useState<Entity>({ theme: "light", live: true }),
    [searchOpen, setSearchOpen] = useState(false),
    [sidebarWidth, setSidebarWidth] = useState(224);
  const [isMobile, setIsMobile] = useState(false);
  const [navigationHint, setNavigationHint] = useState<{label:string;left:number;top:number} | null>(null);
  // With a mouse, the sidebar shows icons until the pointer or focus is on it, then
  // expands over the page. A width already narrowed to icons stays as chosen.
  const [canHover, setCanHover] = useState(false);
  useEffect(() => {
    const media = window.matchMedia("(hover: hover) and (pointer: fine)");
    const update = () => setCanHover(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  const autoCollapse = !isMobile && canHover && sidebarWidth > 112;
  const sidebarPeek = useAutoCollapse({ enabled: autoCollapse });
  const iconNavigation = !isMobile && (sidebarWidth <= 112 || (autoCollapse && !sidebarPeek.expanded));
  useEffect(() => setNavigationHint(null), [sidebarWidth, isMobile, view, iconNavigation]);
  function showNavigationHint(target: EventTarget | null) {
    const link = target instanceof Element ? target.closest<HTMLAnchorElement>("a[aria-label]") : null;
    if (!iconNavigation || !link) { setNavigationHint(null); return; }
    const bounds = link.getBoundingClientRect();
    setNavigationHint({label:link.getAttribute("aria-label")!,left:bounds.right+12,top:Math.max(24,Math.min(window.innerHeight-24,bounds.top+bounds.height/2))});
  }
  const navWasOpen = useRef(false);
  const toggleRef = useRef<HTMLButtonElement>(null),
    sidebarRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const media = window.matchMedia("(max-width: 960px)");
    const change = () => {
      setIsMobile(media.matches);
      if (!media.matches) setNavOpen(false);
    };
    change();
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, []);
  useEffect(() => {
    if (!isMobile) return;
    if (!navOpen) {
      if (navWasOpen.current) toggleRef.current?.focus();
      navWasOpen.current = false;
      return;
    }
    navWasOpen.current = true;
    const first =
      sidebarRef.current?.querySelector<HTMLElement>('[aria-current="page"]') ||
      sidebarRef.current?.querySelector<HTMLElement>("a,button");
    first?.focus();
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setNavOpen(false);
        toggleRef.current?.focus();
      }
      if (event.key === "Tab") {
        const controls = Array.from(
          sidebarRef.current?.querySelectorAll<HTMLElement>(
            "a[href],button:not(:disabled),select",
          ) || [],
        ).filter((node) => node.offsetParent !== null);
        const first = controls[0],
          last = controls.at(-1);
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last?.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first?.focus();
        }
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [isMobile, navOpen]);
  const project = query.get("project") || "",
    resource = query.get("resource") || "";
  const readPreferences = useCallback(() => {
    try {
      setPrefs(readConsolePreferences());
      const width = Number(localStorage.getItem("kionga.sidebar.width"));
      if (width >= 64 && width <= 360) setSidebarWidth(width);
    } catch {
      /* preferences are optional */
    }
  }, []);
  useEffect(() => {
    readPreferences();
    window.addEventListener("kionga:preferences", readPreferences);
    window.addEventListener("storage", readPreferences);
    return () => {
      window.removeEventListener("kionga:preferences", readPreferences);
      window.removeEventListener("storage", readPreferences);
    };
  }, [readPreferences]);
  useEffect(() => {
    document.documentElement.dataset.theme = prefs.theme === "dark" ? "dark" : "light";
    document.body.classList.toggle("compact-ui", !!prefs.compact);
    document.body.classList.toggle("reduce-motion", !!prefs.reduceMotion);
  }, [prefs]);
  useEffect(() => {
    if (!me.data) return;
    if (!prefs.live || !hasService(identity, "overview")) {
      setLive("Live updates off");
      return;
    }
    const source = new EventSource("/api/v1/events");
    let digest = "";
    source.onmessage = (event) => {
      setLive("Live");
      if (digest && digest !== event.data && !dirty)
        window.dispatchEvent(new Event("kionga:refresh"));
      digest = event.data;
    };
    source.onerror = () => setLive("Reconnecting");
    return () => source.close();
  }, [me.data, prefs.live, dirty]);
  useEffect(() => {
    if (me.status === 401)
      location.assign(
        `/auth/login?return_to=${encodeURIComponent(location.pathname + location.search)}`,
      );
  }, [me.status]);
  useEffect(() => {
    const keys = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setSearchOpen((old) => !old);
      }
    };
    window.addEventListener("keydown", keys);
    return () => window.removeEventListener("keydown", keys);
  }, []);
  useEffect(() => {
    if (me.data && view === "overview" && !hasService(identity, "overview"))
      route.replace("/console/profile");
  }, [me.data, view, route]);
  function url(target: string, id = "", targetProject = project) {
    const next = new URLSearchParams();
    if (targetProject) next.set("project", targetProject);
    if (id) next.set("resource", id);
    return `${target === "overview" ? "/console" : `/console/${target}`}${next.size ? `?${next}` : ""}`;
  }
  function navigate(target: string, id = "", targetProject = project) {
    if (
      dirty &&
      !window.confirm("Discard unsaved changes before leaving this screen?")
    )
      return;
    setDirty(false);
    setNavOpen(false);
    setSearchOpen(false);
    route.push(url(target, id, targetProject));
  }
  function selectProject(value: string) {
    if (
      dirty &&
      !window.confirm("Discard unsaved changes before changing projects?")
    )
      return;
    const next = new URLSearchParams(query.toString());
    if (value) next.set("project", value);
    else next.delete("project");
    next.delete("resource");
    setDirty(false);
    route.push(`${pathname}${next.size ? `?${next}` : ""}`);
  }
  const allowed = (target: string) =>
    ungated.has(target) ||
    (target === "access"
      ? isAdministrator(identity)
      : hasService(identity, service(target)));
  const props = { view, identity, project, resource, onNavigate: navigate };
  const title =
    view === "pipeline-editor" ? "Pipeline editor" : titles[view] || "Console";
  function resize(width: number) {
    const value = Math.max(64, Math.min(360, Math.round(width)));
    setSidebarWidth(value);
    try { localStorage.setItem("kionga.sidebar.width", String(value)); } catch { /* resizing remains available without storage */ }
  }
  return (
    <UnsavedContext.Provider value={setDirty}>
      <div
        className={`app-shell${navOpen ? " nav-open" : ""}${iconNavigation ? " nav-icons" : ""}${autoCollapse ? " nav-auto" : ""}${sidebarPeek.expanded ? " nav-peek" : ""}`}
        style={{
          // Auto-collapse reserves only the icon column, so expanding never moves the page.
          "--sidebar-width": `${autoCollapse ? 64 : sidebarWidth}px`,
          "--sidebar-panel": `${sidebarWidth}px`,
        } as CSSProperties}
      >
        <a className="skip-link" href="#main">
          Skip to content
        </a>
        <aside
          ref={sidebarRef}
          inert={isMobile && !navOpen}
          className="app-sidebar"
          id="sidebar"
          aria-label="Console navigation"
          onPointerOver={(event) => showNavigationHint(event.target)}
          onPointerEnter={sidebarPeek.handlers.onPointerEnter}
          onPointerLeave={(event) => { setNavigationHint(null); sidebarPeek.handlers.onPointerLeave(event); }}
          onFocus={(event) => { showNavigationHint(event.target); sidebarPeek.handlers.onFocus(); }}
          onBlur={(event) => { setNavigationHint(null); sidebarPeek.handlers.onBlur(event); }}
          onClick={() => setNavigationHint(null)}
          onScroll={() => setNavigationHint(null)}
          onKeyDown={(event) => { if (event.key === "Escape") setNavigationHint(null); }}
        >
          <Link
            className="wordmark"
            aria-label="Kionga overview"
            href="/console"
            onClick={(event) => {
              event.preventDefault();
              navigate("overview");
            }}
          >
            <img src="/logo.svg" width={30} height={30} alt="" />
            <span>Kionga</span>
          </Link>
          <button
            className="mobile-menu icon-button"
            aria-label="Close navigation"
            title="Close navigation"
            onClick={() => {
              setNavOpen(false);
              toggleRef.current?.focus();
            }}
          >
            <Icon name="close" />
          </button>
          <nav className="sidebar-nav" aria-label="Platform">
            {groups.map((group) => (
              <section
                className="nav-group"
                key={group.label}
                aria-labelledby={`nav-${group.views[0][0]}`}
              >
                <h2 className="nav-group-label" id={`nav-${group.views[0][0]}`}>
                  {group.label}
                </h2>
                {group.views
                  .filter(([id]) => allowed(id))
                  .map(([id, label]) => (
                    <Link
                      key={id}
                      data-view={id}
                      aria-label={label}
                      href={url(id)}
                      onMouseEnter={() => route.prefetch(url(id))}
                      onFocus={() => route.prefetch(url(id))}
                      aria-current={view === id ? "page" : undefined}
                      onClick={(event) => {
                        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                        event.preventDefault();
                        navigate(id);
                      }}
                    >
                      <Icon name={id} />
                      <span>{label}</span>
                    </Link>
                  ))}
              </section>
            ))}
          </nav>
          <div className="workspace-links">
            {["workbench", "ide"]
              .filter((tool) => hasService(identity, tool))
              .map((tool) => (
                <a
                  id={`${tool === "workbench" ? "workbench" : "ide"}-link`}
                  key={tool}
                  aria-label={tool === "workbench" ? "JupyterLab" : "Browser IDE"}
                  href={`/workspace?tool=${tool}${project ? `&project=${encodeURIComponent(project)}` : ""}`}
                >
                  <Icon name={tool} />
                  <span>
                    {tool === "workbench" ? "JupyterLab" : "Browser IDE"}
                  </span>
                </a>
              ))}
            {hasService(identity, "langflow") && (
              <Link id="langflow-link" aria-label="Langflow canvas" href={url("langflow")} onClick={(event) => {
                if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                event.preventDefault();
                navigate("langflow");
              }}>
                <Icon name="langflow" />
                <span>Langflow canvas</span>
              </Link>
            )}
          </div>
          <button
            className="sidebar-resizer"
            aria-label="Navigation width"
            role="separator"
            aria-orientation="vertical"
            aria-valuemin={64}
            aria-valuetext={iconNavigation ? `${sidebarWidth} pixels, icons only` : `${sidebarWidth} pixels`}
            aria-valuemax={360}
            aria-valuenow={sidebarWidth}
            onDoubleClick={() => resize(224)}
            onKeyDown={(event) => {
              if (!["ArrowRight", "ArrowLeft", "Home", "End"].includes(event.key)) return;
              event.preventDefault();
              if (event.key === "ArrowRight") resize(sidebarWidth + 10);
              if (event.key === "ArrowLeft") resize(sidebarWidth - 10);
              if (event.key === "Home") resize(64);
              if (event.key === "End") resize(360);
            }}
            onPointerDown={(event) => {
              event.preventDefault();
              const element = event.currentTarget;
              const startX = event.clientX, startWidth = sidebarWidth;
              element.setPointerCapture(event.pointerId);
              sidebarPeek.hold(true);
              const move = (e: PointerEvent) => resize(startWidth + e.clientX - startX);
              const stop = () => {
                sidebarPeek.hold(false);
                element.removeEventListener("pointermove", move);
                element.removeEventListener("pointerup", stop);
                element.removeEventListener("pointercancel", stop);
                element.removeEventListener("lostpointercapture", stop);
              };
              element.addEventListener("pointermove", move);
              element.addEventListener("pointerup", stop);
              element.addEventListener("pointercancel", stop);
              element.addEventListener("lostpointercapture", stop);
            }}
          />
        </aside>
        {navigationHint && createPortal(<div className="sidebar-tooltip" role="tooltip" style={{left:navigationHint.left,top:navigationHint.top}}>{navigationHint.label}</div>,document.body)}
        <div className="app-content" inert={isMobile && navOpen}>
          <header className="app-topbar">
            <button
              className="mobile-menu"
              id="sidebar-toggle"
              ref={toggleRef}
              aria-controls="sidebar"
              aria-expanded={navOpen}
              onClick={() => setNavOpen(!navOpen)}
            >
              <Icon name={navOpen ? "close" : "menu"} />
              <span>{navOpen ? "Close navigation" : "Navigation"}</span>
            </button>
            <button
              className="resource-search-trigger"
              aria-label="Search resources"
              onClick={() => setSearchOpen(true)}
              aria-keyshortcuts="Control+k Meta+k"
            >
              <Icon name="search" />
              <span className="search-label-long">Search resources</span><span className="search-label-short">Search</span>
              <kbd>⌘ K</kbd>
            </button>
            <span id="live-indicator" role="status">
              {live}
            </span>
            <a className="topbar-logout" href="/auth/logout">
              <Icon name="logout" />
              <span>Log out</span>
            </a>
          </header>
          <main className="app-main" id="main">
            <SessionNotice />
            <header className="page-header console-page-header">
              <div>
                <h1 id="page-title">{title}</h1>
                <span>{identity.email || identity.subject}</span>
              </div>
              <div className="toolbar">
                {[
                  "overview",
                  "projects",
                  "pipelines",
                  "functions",
                  "models",
                  "agents",
                  "agent-designer",
                  "agent-definitions",
                  "agent-teams",
                  "team-runs",
                  "pipeline-editor",
                  "langflow",
                  "logs",
                ].includes(view) && (
                  <label className="project-filter">
                    Project
                    <Select
                      id="project-context"
                      value={project}
                      onChange={(event) => selectProject(event.target.value)}
                    >
                      <option value="">All assigned projects</option>
                      {items(projects.data).map((item) => (
                        <option key={item.id} value={item.id}>
                          {item.name}
                        </option>
                      ))}
                    </Select>
                  </label>
                )}
                <button
                  id="refresh-view"
                  aria-label="Refresh this page"
                  onClick={() => {
                    me.reload();
                    projects.reload();
                    workspaceQuery.reload();
                    window.dispatchEvent(new Event("kionga:refresh"));
                  }}
                >
                  <Icon name="refresh" />
                  <span>Refresh</span>
                </button>
              </div>
            </header>
            <DataState
              loading={me.loading && !me.data}
              error={me.error}
              onRetry={me.reload}
            >
              {!allowed(view) ? (
                <section className="feedback" role="alert">
                  <h2>Access required</h2>
                  <p>Your account has not been assigned this service.</p>
                  <button onClick={() => navigate("profile")}>
                    Request access
                  </button>
                </section>
              ) : view === "langflow" ? (
                <LangflowView {...props} />
              ) : view === "agents" ? (
                <AgentsView {...props} />
              ) : view === "agent-designer" ? (
                <AgentDesigner {...props} />
              ) : view === "agent-definitions" ? (
                <AgentDefinitionsView {...props} />
              ) : view === "agent-teams" ? (
                <TeamsView {...props} />
              ) : view === "team-runs" ? (
                <TeamRunsView {...props} />
              ) : view === "pipeline-editor" ? (
                <PipelineEditor {...props} />
              ) : (
                <OperationsView {...props} />
              )}
            </DataState>
          </main>
        </div>
        {searchOpen && (
          <ResourceSearch
            identity={identity}
            project={project}
            projects={items(projects.data)}
            onClose={() => setSearchOpen(false)}
            onSelect={navigate}
          />
        )}
      </div>
    </UnsavedContext.Provider>
  );
}
