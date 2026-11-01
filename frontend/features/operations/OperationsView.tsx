"use client";
import { publicationHref } from "@/lib/publication";
import {
  useResource,
  items,
  State,
  Table,
  date,
  type OperationsProps,
} from "./shared";
import { Overview, Models, Functions } from "./Resources";
import { Features, Storage, Catalog, Realtime } from "./Data";
import { Projects } from "./Projects";
import { Pipelines } from "./Pipelines";
import { Logs } from "./Logs";
import { Platform } from "./Platform";
import { Access } from "./Access";
import { Settings, Profile } from "./Settings";
export function OperationsView(props: OperationsProps) {
  switch (props.view) {
    case "overview":
      return <Overview {...props} />;
    case "projects":
      return <Projects {...props} />;
    case "pipelines":
      return <Pipelines {...props} />;
    case "logs":
      return <Logs project={props.project} />;
    case "models":
      return <Models {...props} />;
    case "functions":
      return <Functions {...props} />;
    case "features":
      return <Features />;
    case "storage":
      return <Storage {...props} />;
    case "catalog":
      return <Catalog />;
    case "realtime":
      return <Realtime />;
    case "platform":
      return <Platform {...props} />;
    case "access":
      return <Access {...props} />;
    case "profile":
      return <Profile {...props} />;
    case "settings":
      return <Settings {...props} />;
    case "blogs":
      return <PublishedBlogs />;
    default:
      return <p className="empty-state">Choose a section from navigation.</p>;
  }
}
function PublishedBlogs() {
  const query = useResource("/api/v1/blogs");
  return (
    <>
      <header className="page-header">
        <p>
          Your organization’s publication. Editorial membership governs writing
          and publishing.
        </p>
        <div className="button-row">
          <a href={publicationHref()}>View publication</a>
          <a href="/studio">Writing studio</a>
        </div>
      </header>
      <State {...query} retry={query.reload}>
        <Table
          rows={items(query.data)}
          columns={[
            { key: "title", label: "Article" },
            { key: "author", label: "Author" },
            {
              key: "tags",
              label: "Tags",
              render: (row) => (row.tags || []).join(", "),
            },
            {
              key: "published_at",
              label: "Published",
              render: (row) => date(row.published_at || row.created_at),
            },
          ]}
          actions={(row) => (
            <a
              href={publicationHref(
                `/blog/read?slug=${encodeURIComponent(row.slug)}`,
              )}
            >
              Read article
            </a>
          )}
        />
      </State>
    </>
  );
}
