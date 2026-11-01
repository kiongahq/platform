"use client";
import {publicationHref} from "@/lib/publication";
import {Select} from "@/components/ui/Select";
import React, { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  editorial,
  uploadMedia,
  editURL,
  roles,
  statuses,
  type EditorialPost,
  type Media,
  type Member,
} from "@/lib/editorial";
import { useStudio, type StudioView } from "./StudioShell";
const date = (value: string) => new Date(value).toLocaleString();
function useLoad<T>(path: string) {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const reload = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setData(await editorial<T>(path));
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setLoading(false);
    }
  }, [path]);
  useEffect(() => {
    void reload();
  }, [reload]);
  return { data, error, loading, reload };
}
function State({
  loading,
  error,
  retry,
}: {
  loading: boolean;
  error: string;
  retry: () => void;
}) {
  return loading ? (
    <p role="status">Loading…</p>
  ) : error ? (
    <p role="alert" className="feedback error">
      {error}
      <button onClick={retry}>Retry</button>
    </p>
  ) : null;
}
export function PostsView({ view }: { view: StudioView }) {
  const { session } = useStudio();
  const [filter, setFilter] = useState("all");
  useEffect(() => {
    setFilter(new URLSearchParams(location.search).get("status") || "all");
  }, []);
  const params = new URLSearchParams();
  if (view === "drafts") {
    params.set("status", "draft");
    if (session.member.role === "author") params.set("mine", "1");
  }
  if (view === "reviews") params.set("status", "in_review");
  const { data, error, loading, reload } = useLoad<{ items: EditorialPost[] }>(
    `/api/v1/editorial/posts?${params}`,
  );
  const title =
    view === "drafts" ? "Drafts" : view === "reviews" ? "Reviews" : "Posts";
  const items = (data?.items || []).filter(
    (post) => filter === "all" || post.status === filter,
  );
  return (
    <>
      <header className="page-header">
        <div>
          <h1>{title}</h1>
          <p>
            {view === "reviews"
              ? "Review submitted articles and choose their next publishing action."
              : "Articles and publication status."}
          </p>
        </div>
        <Link className="button primary" id="ed-new-post" href="/studio/new">
          New post
        </Link>
      </header>
      {view === "posts" && (
        <div className="tabs" role="group" aria-label="Filter by status">
          {["all", ...Object.keys(statuses)].map((status) => (
            <button
              type="button"
              key={status}
              aria-pressed={filter === status}
              onClick={() => {
                setFilter(status);
                history.replaceState(
                  null,
                  "",
                  `${location.pathname}${status === "all" ? "" : `?status=${status}`}`,
                );
              }}
            >
              {status === "all" ? "All" : statuses[status]}
            </button>
          ))}
        </div>
      )}
      <State loading={loading} error={error} retry={() => void reload()} />
      {!loading &&
        !error &&
        (items.length ? (
          <ul className="ed-post-list" role="list">
            {items.map((post) => (
              <li key={post.id} className="panel ed-post-row">
                <div>
                  <Link className="ed-post-title" href={editURL(post.id)}>
                    {post.title || "Untitled draft"}
                  </Link>
                  <p>{post.summary || "No summary yet."}</p>
                  <div className="post-meta">
                    <span>{post.author}</span>
                    <code>/{post.slug}</code>
                    <span>Updated {date(post.updated_at)}</span>
                    {post.publish_at && post.status === "scheduled" && (
                      <time>Publishes {date(post.publish_at)}</time>
                    )}
                  </div>
                </div>
                <div className="button-row">
                  <span className="status">
                    {statuses[post.status] || post.status}
                  </span>
                  {post.status === "published" && (
                    <Link
                      href={publicationHref(`/blog/read?slug=${encodeURIComponent(post.slug)}`)}
                    >
                      View
                    </Link>
                  )}
                  <Link href={editURL(post.id)}>
                    {post.can_edit ? "Edit" : "Open"}
                  </Link>
                </div>
              </li>
            ))}
          </ul>
        ) : (
          <section className="empty-state panel">
            <h2>
              {view === "reviews"
                ? "Nothing is waiting for review."
                : "No posts with this status."}
            </h2>
            <Link href="/studio/new">New post</Link>
          </section>
        ))}
    </>
  );
}
function MediaCard({ media, refresh }: { media: Media; refresh: () => void }) {
  const { notify } = useStudio();
  const [value, setValue] = useState(media),
    [busy, setBusy] = useState(false);
  return (
    <li className="panel" data-media={media.id}>
      <img
        src={`/media/blog/${encodeURIComponent(media.id)}/480`}
        alt={value.alt}
        loading="lazy"
        width={media.width}
        height={media.height}
      />
      <p className="post-meta">
        <span>{media.status === "attached" ? "Attached" : "Pending"}</span>
        <span>
          {media.width} × {media.height}
        </span>
        <span>{date(media.created_at)}</span>
      </p>
      {(["alt", "caption", "attribution"] as const).map((key) => (
        <label key={key}>
          {key === "alt"
            ? "Alt text"
            : key === "caption"
              ? "Caption"
              : "Attribution"}
          <input
            value={value[key]}
            maxLength={key === "attribution" ? 200 : 300}
            onChange={(e) => setValue({ ...value, [key]: e.target.value })}
          />
        </label>
      ))}
      <div className="button-row">
        <button
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await editorial(
                `/api/v1/editorial/media/${encodeURIComponent(media.id)}`,
                {
                  method: "PATCH",
                  body: {
                    alt: value.alt,
                    caption: value.caption,
                    attribution: value.attribution,
                  },
                },
              );
              notify("Image details saved.");
              refresh();
            } catch (failure) {
              notify((failure as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          Save details
        </button>
        <button
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(
                `${location.origin}/media/blog/${media.id}/960`,
              );
              notify("Image URL copied.");
            } catch {
              notify(`${location.origin}/media/blog/${media.id}/960`);
            }
          }}
        >
          Copy URL
        </button>
      </div>
    </li>
  );
}
export function MediaView() {
  const { notify } = useStudio();
  const { data, error, loading, reload } = useLoad<{ items: Media[] }>(
    "/api/v1/editorial/media",
  );
  const [uploads, setUploads] = useState<
    { name: string; progress: number; error?: string }[]
  >([]);
  async function send(files: File[]) {
    for (const file of files) {
      setUploads((current) => [...current, { name: file.name, progress: 0 }]);
      try {
        await uploadMedia(file, {
          onProgress: (progress) =>
            setUploads((current) =>
              current.map((row) =>
                row.name === file.name ? { ...row, progress } : row,
              ),
            ),
        });
      } catch (failure) {
        setUploads((current) =>
          current.map((row) =>
            row.name === file.name
              ? { ...row, error: (failure as Error).message }
              : row,
          ),
        );
      }
    }
    await reload();
    notify("Upload finished.");
  }
  return (
    <>
      <header className="page-header">
        <div>
          <h1>Media</h1>
          <p>Images for articles. Unused uploads expire after 24 hours.</p>
        </div>
        <label className="button">
          Upload image
          <input
            type="file"
            className="sr-only"
            multiple
            accept="image/jpeg,image/png,image/webp,image/gif"
            onChange={(e) => void send(Array.from(e.target.files || []))}
          />
        </label>
      </header>
      <div
        className="panel media-drop"
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          e.preventDefault();
          void send(Array.from(e.dataTransfer.files));
        }}
      >
        <p>Drop JPEG, PNG, WebP or GIF images here. Maximum 10 MB each.</p>
        {uploads.map((row, i) => (
          <p key={i}>
            {row.name}
            <progress max={100} value={row.progress} />
            {row.error && <span role="alert">{row.error}</span>}
          </p>
        ))}
      </div>
      <State loading={loading} error={error} retry={() => void reload()} />
      {data && (
        <ul className="media-grid ed-media-grid">
          {data.items.map((media) => (
            <MediaCard
              key={`${media.id}-${media.alt}`}
              media={media}
              refresh={() => void reload()}
            />
          ))}
        </ul>
      )}
      {data && !data.items.length && (
        <p className="empty-state">No images yet.</p>
      )}
    </>
  );
}
export function AuthorsView() {
  const { notify } = useStudio();
  const { data, error, loading, reload } = useLoad<{
    items: Member[];
    can_manage: boolean;
  }>("/api/v1/editorial/members");
  const [busy, setBusy] = useState(false);
  async function put(member: Member, patch: Partial<Member>) {
    setBusy(true);
    try {
      await editorial(
        `/api/v1/editorial/members/${encodeURIComponent(member.subject)}`,
        {
          method: "PUT",
          body: {
            display_name: member.display_name,
            email: member.email || "",
            role: member.role,
            disabled: member.disabled,
            ...patch,
          },
        },
      );
      notify("Membership updated.");
      await reload();
    } catch (failure) {
      notify((failure as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <header className="page-header">
        <div>
          <h1>Authors</h1>
          <p>Editorial membership controls who can write and publish.</p>
        </div>
      </header>
      <State loading={loading} error={error} retry={() => void reload()} />
      {data?.can_manage && (
        <form
          className="panel form-grid"
          onSubmit={async (e) => {
            e.preventDefault();
            const form = e.currentTarget,
              fields = new FormData(form);
            setBusy(true);
            try {
              await editorial(
                `/api/v1/editorial/members/${encodeURIComponent(String(fields.get("subject")).trim())}`,
                {
                  method: "PUT",
                  body: {
                    display_name: fields.get("display_name"),
                    role: fields.get("role"),
                  },
                },
              );
              notify("Member added.");
              form.reset();
              await reload();
            } catch (failure) {
              notify((failure as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <label>
            Platform identity
            <input name="subject" required autoComplete="off" />
          </label>
          <label>
            Display name
            <input name="display_name" required />
          </label>
          <label>
            Role
            <Select name="role">
              {Object.entries(roles).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          </label>
          <button disabled={busy} className="primary">
            Add member
          </button>
        </form>
      )}
      {data && (
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>Member</th>
                <th>Role</th>
                <th>Posts</th>
                <th>Status</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((member) => (
                <tr key={member.subject}>
                  <td>
                    <strong>{member.display_name}</strong>
                    <small>
                      {member.subject}
                      {member.email && ` (${member.email})`}
                    </small>
                  </td>
                  <td>
                    {data.can_manage ? (
                      <Select
                        aria-label={`Role for ${member.display_name}`}
                        value={member.role}
                        disabled={busy}
                        onChange={(e) =>
                          void put(member, {
                            role: e.target.value as Member["role"],
                          })
                        }
                      >
                        {Object.entries(roles).map(([value, label]) => (
                          <option value={value} key={value}>
                            {label}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      roles[member.role]
                    )}
                  </td>
                  <td>{member.posts || 0}</td>
                  <td>{member.disabled ? "Suspended" : "Active"}</td>
                  <td>
                    {data.can_manage ? (
                      <div className="button-row">
                        <button
                          disabled={busy}
                          onClick={() =>
                            void put(member, { disabled: !member.disabled })
                          }
                        >
                          {member.disabled ? "Reactivate" : "Suspend"}
                        </button>
                        <button
                          className="danger"
                          disabled={busy}
                          onClick={async () => {
                            if (
                              !confirm(
                                `Remove ${member.subject} from the editorial workspace?`,
                              )
                            )
                              return;
                            try {
                              await editorial(
                                `/api/v1/editorial/members/${encodeURIComponent(member.subject)}`,
                                { method: "DELETE" },
                              );
                              await reload();
                              notify("Member removed.");
                            } catch (failure) {
                              notify((failure as Error).message);
                            }
                          }}
                        >
                          Remove
                        </button>
                      </div>
                    ) : (
                      <Link
                        href={publicationHref(`/blog?author=${encodeURIComponent(member.display_name)}`)}
                      >
                        Posts
                      </Link>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
export function SettingsView() {
  const { session, notify } = useStudio();
  const { data, error, loading, reload } = useLoad<{
    title: string;
    description: string;
  }>("/api/v1/editorial/settings");
  const [value, setValue] = useState({ title: "", description: "" });
  useEffect(() => {
    if (data) setValue(data);
  }, [data]);
  const admin = session.member.role === "editorial_admin";
  return (
    <>
      <header className="page-header">
        <h1>Settings</h1>
      </header>
      <State loading={loading} error={error} retry={() => void reload()} />
      {data && (
        <form
          className="panel"
          onSubmit={async (e) => {
            e.preventDefault();
            try {
              await editorial("/api/v1/editorial/settings", {
                method: "PUT",
                body: value,
              });
              notify("Settings saved.");
            } catch (failure) {
              notify((failure as Error).message);
            }
          }}
        >
          <h2>Blog identity</h2>
          <label>
            Blog headline
            <input
              maxLength={120}
              disabled={!admin}
              value={value.title}
              onChange={(e) => setValue({ ...value, title: e.target.value })}
            />
          </label>
          <label>
            Description
            <textarea
              rows={3}
              maxLength={400}
              disabled={!admin}
              value={value.description}
              onChange={(e) =>
                setValue({ ...value, description: e.target.value })
              }
            />
          </label>
          {admin ? (
            <button className="primary">Save settings</button>
          ) : (
            <p>Only editorial administrators can change these settings.</p>
          )}
        </form>
      )}
      <section className="panel">
        <h2>Workspace</h2>
        <dl>
          <dt>Your role</dt>
          <dd>{roles[session.member.role]}</dd>
          <dt>Session</dt>
          <dd>
            {session.limits?.session_hours || 4} hours, rechecked on each
            request
          </dd>
          <dt>Media storage</dt>
          <dd>
            {session.media_backend === "s3"
              ? "Object storage"
              : "Local filesystem"}
          </dd>
          <dt>Upload limit</dt>
          <dd>
            {Math.round(
              (session.limits?.max_upload_bytes || 10485760) / 1048576,
            )}{" "}
            MB
          </dd>
          <dt>Image sizes</dt>
          <dd>
            {(session.limits?.variant_widths || [480, 960, 1600]).join(", ")} px
          </dd>
        </dl>
      </section>
    </>
  );
}
