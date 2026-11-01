"use client";
import { Select } from "@/components/ui/Select";
import { useEffect, useId, useMemo, useState } from "react";
import { request, errorMessage } from "@/lib/api";
import { hasService, items, type Identity, type Entity } from "@/lib/types";
import { Modal } from "@/features/shared/View";
import { Icon } from "@/components/ui/Icon";

type SearchItem = {
  id: string;
  name: string;
  project: string;
  type: string;
  view: string;
  detail: string;
};
const sources = [
  { view: "projects", type: "Project", path: "/api/v1/projects" },
  {
    view: "pipelines",
    type: "Pipeline",
    path: "/api/v1/pipelines/definitions",
  },
  { view: "models", type: "Model", path: "/api/v1/models" },
  { view: "agents", type: "Agent", path: "/api/v1/agents" },
  { view: "functions", type: "Function", path: "/api/v1/functions" },
];
export function ResourceSearch({
  identity,
  project,
  projects,
  onClose,
  onSelect,
}: {
  identity: Identity;
  project: string;
  projects: Entity[];
  onClose: () => void;
  onSelect: (view: string, id: string, project: string) => void;
}) {
  const [query, setQuery] = useState(""),
    [scope, setScope] = useState(project),
    [kind, setKind] = useState("all"),
    [loaded, setLoaded] = useState<SearchItem[]>([]),
    [errors, setErrors] = useState<string[]>([]),
    [loading, setLoading] = useState(true),
    [retry, setRetry] = useState(0),
    [active, setActive] = useState(0);
  const id = useId();
  const permitted = sources.filter((source) =>
    hasService(identity, source.view),
  );
  const sourceKey = permitted.map((source) => source.view).join(",");
  useEffect(() => {
    const abort = new AbortController();
    setLoading(true);
    setErrors([]);
    setLoaded([]);
    Promise.all(
      sources
        .filter((source) => sourceKey.split(",").includes(source.view))
        .map(async (source) => {
          try {
            const data = await request(source.path, { signal: abort.signal });
            return {
              rows: items(data).map((item) => ({
                id: String(item.id),
                name: String(item.name || item.title || item.id),
                project: String(
                  item.project_id ||
                    (source.view === "projects" ? item.id : ""),
                ),
                type: source.type,
                view: source.view,
                detail: String(item.status || item.version || ""),
              })),
              error: "",
            };
          } catch (failure) {
            return {
              rows: [],
              error: `${source.type}: ${errorMessage(failure)}`,
            };
          }
        }),
    ).then((results) => {
      if (!abort.signal.aborted) {
        setLoaded(results.flatMap((result) => result.rows));
        setErrors(results.map((result) => result.error).filter(Boolean));
        setLoading(false);
      }
    });
    return () => abort.abort();
  }, [sourceKey, retry]);
  const projectNames = useMemo(
    () => new Map(projects.map((item) => [item.id, item.name])),
    [projects],
  );
  const results = useMemo(
    () =>
      loaded
        .filter(
          (item) =>
            (!scope || item.project === scope) &&
            (kind === "all" || item.view === kind) &&
            `${item.name} ${item.id} ${item.type} ${projectNames.get(item.project) || ""}`
              .toLowerCase()
              .includes(query.toLowerCase().trim()),
        )
        .sort((a, b) => a.name.localeCompare(b.name))
        .slice(0, 60),
    [loaded, scope, kind, query, projectNames],
  );
  useEffect(() => setActive(0), [query, scope, kind]);
  useEffect(() => {
    document
      .getElementById(`${id}-result-${active}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [active, id]);
  const choose = (item: SearchItem) =>
    onSelect(item.view, item.id, item.project);
  return (
    <Modal title="Search resources" onClose={onClose}>
      <div className="resource-search">
        <label className="resource-search-input" htmlFor={`${id}-input`}>
          <span className="sr-only">Search by name or resource ID</span>
          <Icon name="search" />
          <input
            id={`${id}-input`}
            autoFocus
            type="search"
            role="combobox"
            aria-autocomplete="list"
            aria-expanded={true}
            aria-controls={`${id}-results`}
            aria-activedescendant={
              results[active] ? `${id}-result-${active}` : undefined
            }
            placeholder="Search by name or resource ID"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                event.preventDefault();
                setActive((index) =>
                  results.length
                    ? (index +
                        (event.key === "ArrowDown" ? 1 : -1) +
                        results.length) %
                      results.length
                    : 0,
                );
              } else if (event.key === "Enter" && results[active]) {
                event.preventDefault();
                choose(results[active]);
              }
            }}
          />
        </label>
        <div className="resource-search-filters">
          <label>
            Project
            <Select
              value={scope}
              onChange={(event) => setScope(event.target.value)}
            >
              <option value="">All assigned projects</option>
              {projects.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}
                </option>
              ))}
            </Select>
          </label>
          <label>
            Resource type
            <Select
              value={kind}
              onChange={(event) => setKind(event.target.value)}
            >
              <option value="all">All resource types</option>
              {permitted.map((source) => (
                <option key={source.view} value={source.view}>
                  {source.type}
                </option>
              ))}
            </Select>
          </label>
        </div>
        {loading && <p role="status">Loading assigned resources…</p>}
        {!!errors.length && (
          <div className="feedback error">
            <p>Some resources could not be loaded.</p>
            <ul>
              {errors.map((error) => (
                <li key={error}>{error}</li>
              ))}
            </ul>
            <button
              type="button"
              onClick={() => setRetry((value) => value + 1)}
            >
              Retry search
            </button>
          </div>
        )}
        <ul
          className="resource-search-results"
          id={`${id}-results`}
          role="listbox"
          aria-label="Matching resources"
        >
          {results.map((item, index) => (
            <li
              key={`${item.view}-${item.id}`}
              role="presentation"
              onMouseMove={() => setActive(index)}
            >
              <button
                type="button"
                role="option"
                id={`${id}-result-${index}`}
                aria-selected={active === index}
                tabIndex={-1}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => choose(item)}
              >
                <Icon name={item.view} />
                <span>
                  <strong>{item.name}</strong>
                  <small>
                    {item.type}
                    {projectNames.get(item.project)
                      ? ` / ${projectNames.get(item.project)}`
                      : ""}
                  </small>
                  <code>{item.id}</code>
                </span>
                {item.detail && (
                  <span className="resource-search-state">{item.detail}</span>
                )}
              </button>
            </li>
          ))}
        </ul>
        {!loading && !results.length && (
          <p className="empty-state">
            No matching resources. Try another name, ID or project.
          </p>
        )}
        <p className="resource-search-help">
          <span>
            <kbd>↑</kbd> <kbd>↓</kbd> Select
          </span>
          <span>
            <kbd>Enter</kbd> Open resource
          </span>
          <span>
            <kbd>Esc</kbd> Close
          </span>
        </p>
      </div>
    </Modal>
  );
}
