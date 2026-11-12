"use client";
import {CodeEditor} from "@/components/ui/CodeEditor";
import {Icon} from "@/components/ui/Icon";
import React, { useEffect, useRef, useState } from "react";
import UndoIcon from "@mui/icons-material/Undo";
import RedoIcon from "@mui/icons-material/Redo";
import Link from "next/link";
import type { Editor } from "@tiptap/react";
import { Writer } from "./Writer";
import { ArticleBlocks } from "../blog/ArticleBlocks";
import { useStudio } from "./StudioShell";
import { createSaveController, type SaveState } from "@/lib/editorial-save";
import {
  editorial,
  uploadMedia,
  emptyDraft,
  editURL,
  fingerprint,
  readRecovery,
  clearRecovery,
  slugify,
  wordCount,
  actions,
  statuses,
  type Draft,
  type EditorialPost,
  type Media,
  type Recovery,
} from "@/lib/editorial";
import type { Block } from "@/lib/editor-blocks";
function fromPost(post: EditorialPost): Draft {
  return {
    title: post.title || "",
    summary: post.summary || "",
    slug: post.slug || "",
    tags: post.tags || [],
    blocks: post.blocks || [],
    cover: post.cover || null,
    seo: post.seo || {},
    editor_version: "kionga-tiptap-3",
  };
}
function localDate(value?: string) {
  if (!value) return "";
  const d = new Date(value);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}T${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}
function Dialog({
  id,
  title,
  onClose,
  children,
}: {
  id: string;
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
    return () => {
      ref.current?.close();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      id={id}
      className="dialog studio-dialog"
      aria-label={title}
      onCancel={onClose}
    >
      <header className="page-header">
        <h2>{title}</h2>
        <button data-close type="button" className="icon-button" aria-label={`Close ${title}`} title="Close" onClick={onClose}>
          <Icon name="close"/>
        </button>
      </header>
      {children}
    </dialog>
  );
}
export function PostEditor({ isNew = false }: { isNew?: boolean }) {
  const { session } = useStudio();
  const [post, setPost] = useState<EditorialPost | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    if (isNew) {
      setPost({
        ...emptyDraft(),
        id: "",
        revision: 0,
        status: "draft",
        can_edit: true,
        actions: [],
        author: session.member.display_name,
        created_at: "",
        updated_at: "",
      });
      return;
    }
    const id = new URLSearchParams(location.search).get("post");
    if (!id) {
      setError("Choose a post to edit.");
      return;
    }
    editorial<EditorialPost>(
      `/api/v1/editorial/posts/${encodeURIComponent(id)}`,
    )
      .then((value) => {
        if (active) setPost(value);
      })
      .catch((failure) => {
        if (active) setError(failure.message);
      });
    return () => {
      active = false;
    };
  }, [isNew, session.member.display_name]);
  if (error)
    return (
      <section className="feedback error" role="alert">
        <h1>Editor unavailable</h1>
        <p>{error}</p>
        <Link href="/studio/posts">Posts</Link>
      </section>
    );
  return post ? (
    <EditorWork initial={post} />
  ) : (
    <p role="status">Loading post…</p>
  );
}
function EditorWork({ initial }: { initial: EditorialPost }) {
  const { notify, setGuard } = useStudio();
  const [post, setPost] = useState(initial),
    [draft, setDraft] = useState<Draft>(() => fromPost(initial)),
    [saveState, setSaveState] = useState<SaveState>("saved"),
    [saveLabel, setSaveLabel] = useState(
      initial.id
        ? `Saved ${new Date(initial.updated_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })}`
        : "New post",
    ),
    [conflict, setConflict] = useState<EditorialPost | null>(null),
    [recovery, setRecovery] = useState<Recovery | null>(null),
    [loadKey, setLoadKey] = useState(0),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [publishAt, setPublishAt] = useState(localDate(initial.publish_at)),
    [focus, setFocus] = useState(false),
    [preview, setPreview] = useState(false),
    [previewMode, setPreviewMode] = useState<"desktop" | "mobile">("desktop"),
    [historyOpen, setHistoryOpen] = useState(false),
    [revisions, setRevisions] = useState<any[]>([]),
    [selected, setSelected] = useState<number[]>([]),
    [comparison, setComparison] = useState<any>(null),
    [markdownOpen, setMarkdownOpen] = useState(false),
    [markdown, setMarkdown] = useState(""),
    [library, setLibrary] = useState<Media[] | null>(null),
    [tagsText, setTagsText] = useState(initial.tags.join(", "));
  const postRef = useRef(post),
    draftRef = useRef(draft),
    editorRef = useRef<Editor | null>(null),
    manualSlug = useRef(
      !!initial.slug && initial.slug !== slugify(initial.title),
    );
  postRef.current = post;
  draftRef.current = draft;
  const controllerRef = useRef<ReturnType<typeof createSaveController> | null>(
    null,
  );
  if (!controllerRef.current)
    controllerRef.current = createSaveController({
      key: initial.id || "new",
      revision: initial.revision,
      initial: initial.id ? fromPost(initial) : undefined,
      onStatus: (state, label) => {
        setSaveState(state);
        setSaveLabel(label);
      },
      onConflict: (latest) => setConflict(latest),
      onSaved: (saved) => {
        postRef.current = saved;
        setPost(saved);
      },
      save: async (snapshot, revision) => {
        const id = postRef.current.id;
        const saved = await editorial<EditorialPost>(
          id
            ? `/api/v1/editorial/posts/${encodeURIComponent(id)}`
            : "/api/v1/editorial/posts",
          {
            method: id ? "PUT" : "POST",
            body: id ? { ...snapshot, base_revision: revision } : snapshot,
          },
        );
        if (!id) {
          postRef.current = saved;
          controllerRef.current!.rekey(saved.id, saved.revision);
          history.replaceState(null, "", editURL(saved.id));
        }
        return saved;
      },
    });
  const controller = controllerRef.current;
  function change(patch: Partial<Draft>) {
    const next = { ...draftRef.current, ...patch };
    draftRef.current = next;
    setDraft(next);
    if (postRef.current.can_edit) controller.changed(next);
  }
  function replace(value: Draft) {
    draftRef.current = value;
    setDraft(value);
    setTagsText(value.tags.join(", "));
    setLoadKey((key) => key + 1);
  }
  useEffect(() => {
    const offer = readRecovery(initial.id || "new");
    if (offer && fingerprint(offer.snapshot) !== fingerprint(initial))
      setRecovery(offer);
    else if (offer) clearRecovery(initial.id || "new");
  }, [initial]);
  useEffect(() => {
    controller.activate();
    const before = (event: BeforeUnloadEvent) => {
      if (controller.dirty) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    const online = () => void controller.online();
    const keyboard = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s") {
        event.preventDefault();
        if (postRef.current.can_edit) {
          controller.changed(draftRef.current);
          void controller.flush();
        }
      } else if (event.key === "Escape") setFocus(false);
    };
    window.addEventListener("beforeunload", before);
    window.addEventListener("online", online);
    document.addEventListener("keydown", keyboard);
    setGuard(async () => {
      if (!controller.dirty) return true;
      await controller.flush();
      return (
        !controller.dirty ||
        confirm("Leave this post? Unsaved changes are kept on this device.")
      );
    });
    return () => {
      window.removeEventListener("beforeunload", before);
      window.removeEventListener("online", online);
      document.removeEventListener("keydown", keyboard);
      setGuard(null);
      controller.dispose();
      void controller.flush();
    };
  }, [controller, setGuard]);
  async function save() {
    setError("");
    controller.changed(draftRef.current);
    return controller.flush();
  }
  async function transition(action: string) {
    setError("");
    if (postRef.current.can_edit) await save();
    if (["conflict", "offline", "error"].includes(controller.state)) {
      setError(
        "Save your latest changes and resolve any conflict before changing publication status.",
      );
      return;
    }
    const current = postRef.current;
    if (!current.id) return;
    const snapshot = draftRef.current;
    if (["publish", "schedule"].includes(action)) {
      const missing = snapshot.blocks.filter(
        (block) =>
          block.type === "image" && !String(block.data.alt || "").trim(),
      );
      if (missing.length) {
        setError(
          `Add alt text to ${missing.length} image${missing.length === 1 ? "" : "s"} before publishing.`,
        );
        return;
      }
      if (snapshot.cover && !snapshot.cover.alt.trim()) {
        setError("Add alt text to the cover image before publishing.");
        return;
      }
    }
    if (action === "schedule" && !publishAt) {
      setError("Choose a publish date and time first.");
      return;
    }
    if (
      ["archive", "unpublish"].includes(action) &&
      !confirm(
        action === "archive"
          ? "Archive this post? It will leave the public blog."
          : "Unpublish this post and return it to draft?",
      )
    )
      return;
    setBusy(true);
    try {
      const updated = await editorial<EditorialPost>(
        `/api/v1/editorial/posts/${encodeURIComponent(current.id)}/transition`,
        {
          method: "POST",
          body: {
            action,
            ...(action === "schedule"
              ? { publish_at: new Date(publishAt).toISOString() }
              : {}),
          },
        },
      );
      postRef.current = updated;
      setPost(updated);
      notify(
        action === "publish"
          ? "Published. The post is live on the blog."
          : action === "schedule"
            ? `Scheduled for ${new Date(publishAt).toLocaleString()}.`
            : `${actions[action]}: done.`,
      );
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function loadHistory() {
    const result = await editorial<{ items: any[] }>(
      `/api/v1/editorial/posts/${encodeURIComponent(postRef.current.id)}/revisions`,
    );
    setRevisions(result.items);
  }
  const words = wordCount(draft.blocks);
  const readonly = !post.can_edit;
  return (
    <div id="ed-editor" className={focus ? "studio-focus" : ""}>
      <h1 className="sr-only">
        {post.id ? `Edit ${post.title || "post"}` : "New post"}
      </h1>
      <div
        className="toolbar studio-editor-bar"
        role="toolbar"
        aria-label="Post tools"
      >
        <Link href="/studio/posts">Posts</Link>
        <span id="ed-post-status" className="status">
          {statuses[post.status] || post.status}
        </span>
        <span
          id="ed-save-status"
          role="status"
          aria-live="polite"
          data-state={saveState}
        >
          {saveLabel}
        </span>
        <button
          type="button"
          id="ed-undo"
          title="Undo"
          className="icon-button"
          aria-label="Undo"
          disabled={readonly}
          onClick={() => editorRef.current?.chain().focus().undo().run()}
        >
          <UndoIcon fontSize="small" aria-hidden="true" />
        </button>
        <button
          type="button"
          id="ed-redo"
          title="Redo"
          className="icon-button"
          aria-label="Redo"
          disabled={readonly}
          onClick={() => editorRef.current?.chain().focus().redo().run()}
        >
          <RedoIcon fontSize="small" aria-hidden="true" />
        </button>
        <button
          id="ed-preview"
          type="button"
          onClick={async () => {
            try {
              await editorial("/api/v1/editorial/preview", {
                method: "POST",
                body: draftRef.current,
              });
              setPreview(true);
            } catch (failure) {
              setError((failure as Error).message);
            }
          }}
        >
          Preview
        </button>
        <button
          id="ed-history"
          type="button"
          disabled={!post.id}
          onClick={async () => {
            try {
              setComparison(null);
              setSelected([]);
              await loadHistory();
              setHistoryOpen(true);
            } catch (failure) {
              setError((failure as Error).message);
            }
          }}
        >
          History
        </button>
        <button
          id="ed-focus"
          type="button"
          aria-pressed={focus}
          onClick={() => setFocus(!focus)}
        >
          {focus ? "Exit focus" : "Focus"}
        </button>
        <button
          id="ed-save"
          type="button"
          disabled={readonly || saveState === "saving"}
          onClick={() => void save()}
        >
          Save draft
        </button>
      </div>
      {recovery && !readonly && (
        <div id="ed-recovery" className="feedback">
          <p>
            Unsaved changes from {new Date(recovery.saved_at).toLocaleString()}{" "}
            were found on this device.
            {recovery.base_revision < post.revision
              ? " The server has a newer revision; restoring will save a new revision on top."
              : ""}
          </p>
          <div className="button-row">
            <button
              onClick={() => {
                replace(recovery.snapshot);
                controller.changed(recovery.snapshot);
                setRecovery(null);
                void controller.flush();
              }}
            >
              Restore my changes
            </button>
            <button
              onClick={() => {
                clearRecovery(post.id || "new");
                setRecovery(null);
              }}
            >
              Discard
            </button>
          </div>
        </div>
      )}
      {conflict && (
        <div id="ed-conflict" className="feedback error" role="alert">
          <p>
            Someone saved revision {conflict.revision} while you were editing.
            Your changes are kept on this device.
          </p>
          <div className="button-row">
            <button
              onClick={() => {
                controller.resolve(conflict, true);
                setConflict(null);
                void controller.flush();
              }}
            >
              Keep mine (save on top)
            </button>
            <button
              onClick={() => {
                controller.resolve(conflict, false);
                postRef.current = conflict;
                setPost(conflict);
                replace(fromPost(conflict));
                setConflict(null);
              }}
            >
              Load their version
            </button>
          </div>
        </div>
      )}
      {error && (
        <p id="ed-action-error" className="feedback error" role="alert">
          {error}
        </p>
      )}
      <div className="studio-editor-grid">
        <article className="panel studio-canvas">
          <label className="sr-only" htmlFor="ed-title">
            Title
          </label>
          <textarea
            id="ed-title"
            className="studio-title"
            rows={2}
            maxLength={200}
            placeholder="Post title"
            disabled={readonly}
            value={draft.title}
            onChange={(event) => {
              const title = event.target.value.replace(/\n/g, " ");
              change({
                title,
                ...(!manualSlug.current ? { slug: slugify(title) } : {}),
              });
            }}
          />
          <label className="sr-only" htmlFor="ed-summary">
            Summary
          </label>
          <textarea
            id="ed-summary"
            className="studio-summary"
            rows={3}
            maxLength={600}
            placeholder="What will readers learn?"
            disabled={readonly}
            value={draft.summary}
            onChange={(event) => change({ summary: event.target.value })}
          />
          <div id="ed-holder">
            <Writer
              blocks={draft.blocks}
              readOnly={readonly}
              loadKey={loadKey}
              onChange={(blocks) => change({ blocks })}
              onReady={(editor) => {
                editorRef.current = editor;
              }}
              notify={notify}
            />
          </div>
        </article>
        <aside className="studio-details" aria-label="Post details">
          <section className="panel">
            <h2>Publishing</h2>
            <p>By {post.author}</p>
            <p id="ed-revision">
              {post.revision ? `Revision ${post.revision}` : "Not saved yet"}
            </p>
            <label>
              Publish date and time
              <input
                id="ed-publish-at"
                type="datetime-local"
                value={publishAt}
                onChange={(e) => setPublishAt(e.target.value)}
              />
            </label>
            <p className="field-help">
              Scheduled times use your local timezone.
            </p>
            <div id="ed-actions" className="button-row">
              {(post.actions || []).length ? (
                post.actions.map((action) => (
                  <button
                    key={action}
                    type="button"
                    data-action={action}
                    className={
                      action === "publish"
                        ? "primary"
                        : ["archive", "unpublish"].includes(action)
                          ? "danger"
                          : ""
                    }
                    disabled={busy}
                    onClick={() => void transition(action)}
                  >
                    {actions[action] || action}
                  </button>
                ))
              ) : (
                <p>
                  {post.id
                    ? "No publication actions are available for this post."
                    : "Save the draft to unlock workflow actions."}
                </p>
              )}
            </div>
          </section>
          <section className="panel">
            <h2>URL and tags</h2>
            <label>
              Slug
              <input
                id="ed-slug"
                value={draft.slug}
                maxLength={80}
                disabled={readonly}
                onChange={(e) => {
                  manualSlug.current = !!e.target.value.trim();
                  change({ slug: e.target.value });
                }}
                onBlur={() => change({ slug: slugify(draft.slug) })}
              />
            </label>
            <small>/blog/read?slug={draft.slug || "post-title"}</small>
            <label>
              Tags
              <input
                id="ed-tags"
                value={tagsText}
                disabled={readonly}
                onChange={(e) => {
                  setTagsText(e.target.value);
                  change({
                    tags: e.target.value
                      .split(",")
                      .map((tag) => tag.trim())
                      .filter(Boolean)
                      .slice(0, 12),
                  });
                }}
              />
            </label>
            <small>Comma separated, up to 12.</small>
          </section>
          <section className="panel">
            <h2>Cover image</h2>
            {draft.cover ? (
              <img
                src={`/media/blog/${encodeURIComponent(draft.cover.media_id)}/480`}
                alt={draft.cover.alt}
              />
            ) : (
              <p>No cover selected.</p>
            )}
            <label className="button">
              Upload cover
              <input
                id="ed-cover-file"
                className="sr-only"
                type="file"
                disabled={readonly}
                accept="image/jpeg,image/png,image/webp,image/gif"
                onChange={async (e) => {
                  const file = e.target.files?.[0];
                  if (!file) return;
                  try {
                    const result = await uploadMedia(file);
                    change({
                      cover: {
                        media_id: result.file.media_id,
                        alt: draftRef.current.cover?.alt || "",
                      },
                    });
                  } catch (failure) {
                    setError((failure as Error).message);
                  } finally {
                    e.target.value = "";
                  }
                }}
              />
            </label>
            <div className="button-row">
              <button
                id="ed-cover-pick"
                disabled={readonly}
                onClick={async () => {
                  try {
                    const result = await editorial<{ items: Media[] }>(
                      "/api/v1/editorial/media",
                    );
                    setLibrary(result.items);
                  } catch (failure) {
                    setError((failure as Error).message);
                  }
                }}
              >
                Choose from library
              </button>
              <button
                id="ed-cover-remove"
                disabled={readonly || !draft.cover}
                onClick={() => change({ cover: null })}
              >
                Remove
              </button>
            </div>
            <label>
              Cover alt text
              <input
                id="ed-cover-alt"
                maxLength={300}
                disabled={readonly || !draft.cover}
                value={draft.cover?.alt || ""}
                onChange={(e) =>
                  draft.cover &&
                  change({ cover: { ...draft.cover, alt: e.target.value } })
                }
              />
            </label>
          </section>
          <section className="panel">
            <h2>Search and social</h2>
            <label>
              SEO title
              <input
                id="ed-seo-title"
                maxLength={120}
                disabled={readonly}
                value={draft.seo.title || ""}
                placeholder="Defaults to the post title"
                onChange={(e) =>
                  change({ seo: { ...draft.seo, title: e.target.value } })
                }
              />
            </label>
            <small>{(draft.seo.title || "").length}/60 recommended</small>
            <label>
              SEO description
              <textarea
                id="ed-seo-description"
                rows={3}
                maxLength={320}
                disabled={readonly}
                value={draft.seo.description || ""}
                placeholder="Defaults to the summary"
                onChange={(e) =>
                  change({ seo: { ...draft.seo, description: e.target.value } })
                }
              />
            </label>
            <small>
              {(draft.seo.description || "").length}/160 recommended
            </small>
            <div aria-label="Social card preview">
              <strong>{draft.seo.title || draft.title || "Post title"}</strong>
              <p>{draft.seo.description || draft.summary}</p>
            </div>
          </section>
          <section className="panel">
            <h2>Content</h2>
            <p id="ed-words">{words} words</p>
            <p id="ed-reading">
              {Math.max(1, Math.ceil(words / 220))} min read
            </p>
            <div className="button-row">
              <button
                id="ed-import"
                disabled={readonly}
                onClick={() => setMarkdownOpen(true)}
              >
                Import Markdown
              </button>
              {post.id && (
                <a
                  href={`/api/v1/editorial/posts/${encodeURIComponent(post.id)}/markdown`}
                >
                  Export Markdown
                </a>
              )}
            </div>
          </section>
        </aside>
      </div>
      {preview && (
        <Dialog
          id="ed-preview-dialog"
          title="Preview"
          onClose={() => setPreview(false)}
        >
          <div className="tabs" role="group" aria-label="Preview width">
            {(["desktop", "mobile"] as const).map((mode) => (
              <button
                key={mode}
                data-preview-mode={mode}
                aria-pressed={previewMode === mode}
                onClick={() => setPreviewMode(mode)}
              >
                {mode === "desktop" ? "Desktop" : "Mobile 390px"}
              </button>
            ))}
          </div>
          <div
            id="ed-preview-content"
            data-mode={previewMode}
            className={`article-shell preview-${previewMode}`}
          >
            <h1>{draft.title || "Untitled post"}</h1>
            <p>{draft.summary}</p>
            {draft.cover && (
              <img
                src={`/media/blog/${encodeURIComponent(draft.cover.media_id)}/960`}
                alt={draft.cover.alt}
              />
            )}
            <div className="article-content">
              <ArticleBlocks blocks={draft.blocks} />
            </div>
          </div>
        </Dialog>
      )}
      {historyOpen && (
        <Dialog
          id="ed-history-dialog"
          title="Revision history"
          onClose={() => setHistoryOpen(false)}
        >
          <p>
            Each save is an immutable revision. Select two to compare; restoring
            creates a new revision.
          </p>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Compare</th>
                  <th>Revision</th>
                  <th>Saved</th>
                  <th>By</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {revisions.map((revision) => (
                  <tr key={revision.number}>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`Select revision ${revision.number} to compare`}
                        checked={selected.includes(revision.number)}
                        onChange={(e) =>
                          setSelected((current) =>
                            e.target.checked
                              ? [...current, revision.number]
                              : current.filter((n) => n !== revision.number),
                          )
                        }
                      />
                    </td>
                    <td>
                      #{revision.number}
                      {revision.number === post.revision ? " current" : ""}
                      <small>
                        {revision.reason}
                        {revision.restored_from
                          ? ` of #${revision.restored_from}`
                          : ""}
                      </small>
                    </td>
                    <td>{new Date(revision.created_at).toLocaleString()}</td>
                    <td>{revision.created_by}</td>
                    <td>
                      {revision.number !== post.revision && !readonly && (
                        <button
                          data-restore={revision.number}
                          onClick={async () => {
                            await save();
                            if (
                              ["conflict", "error", "offline"].includes(
                                controller.state,
                              )
                            )
                              return;
                            try {
                              const restored = await editorial<EditorialPost>(
                                `/api/v1/editorial/posts/${encodeURIComponent(postRef.current.id)}/revisions/${revision.number}/restore`,
                                {
                                  method: "POST",
                                  body: { base_revision: controller.revision },
                                },
                              );
                              controller.resolve(restored, false);
                              postRef.current = restored;
                              setPost(restored);
                              replace(fromPost(restored));
                              notify(
                                `Restored revision #${revision.number} as revision #${restored.revision}.`,
                              );
                              await loadHistory();
                            } catch (failure) {
                              setError((failure as Error).message);
                            }
                          }}
                        >
                          Restore
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <button
            onClick={async () => {
              if (selected.length !== 2) {
                notify("Select exactly two revisions.");
                return;
              }
              const [from, to] = [...selected].sort((a, b) => a - b);
              try {
                setComparison(
                  await editorial(
                    `/api/v1/editorial/posts/${encodeURIComponent(post.id)}/compare?from=${from}&to=${to}`,
                  ),
                );
              } catch (failure) {
                notify((failure as Error).message);
              }
            }}
          >
            Compare selected
          </button>
          {comparison && (
            <section id="ed-compare">
              <h3>
                Revision {comparison.from} to {comparison.to}
              </h3>
              <p>
                {comparison.summary.added} added, {comparison.summary.removed}{" "}
                removed, {comparison.summary.changed} changed
              </p>
              {comparison.fields.map((field: any, i: number) => (
                <p key={i}>
                  <strong>{field.field}</strong>{" "}
                  <del>{String(field.before)}</del>{" "}
                  <ins>{String(field.after)}</ins>
                </p>
              ))}
              <ol>
                {comparison.blocks
                  .filter((block: any) => block.change !== "unchanged")
                  .map((block: any, i: number) => (
                    <li key={i}>
                      {block.change}: {block.type}
                      {block.before && <del>{block.before}</del>}
                      {block.after && block.change !== "removed" && (
                        <ins>{block.after}</ins>
                      )}
                    </li>
                  ))}
              </ol>
            </section>
          )}
        </Dialog>
      )}
      {markdownOpen && (
        <Dialog
          id="ed-markdown-dialog"
          title="Import Markdown"
          onClose={() => setMarkdownOpen(false)}
        >
          <form
            onSubmit={async (e) => {
              e.preventDefault();
              try {
                const result = await editorial<{ blocks: Block[] }>(
                  "/api/v1/editorial/markdown",
                  { method: "POST", body: { markdown } },
                );
                const next = {
                  ...draftRef.current,
                  blocks: [...draftRef.current.blocks, ...result.blocks],
                };
                replace(next);
                controller.changed(next);
                setMarkdownOpen(false);
                setMarkdown("");
              } catch (failure) {
                notify((failure as Error).message);
              }
            }}
          >
            <div className="field"><span className="field-caption">Markdown</span><CodeEditor language="markdown" label="Markdown"
                name="markdown"
                rows={14}
                value={markdown}
                onChange={(e) => setMarkdown(e.target.value)}
              />
            </div>
            <button className="primary">Append blocks</button>
          </form>
        </Dialog>
      )}
      {library && (
        <Dialog
          id="ed-cover-library"
          title="Choose cover image"
          onClose={() => setLibrary(null)}
        >
          <div className="media-grid">
            {library.length ? (
              library.map((media) => (
                <button
                  key={media.id}
                  type="button"
                  aria-label={`Choose ${media.alt || media.id}`}
                  onClick={() => {
                    change({ cover: { media_id: media.id, alt: media.alt } });
                    setLibrary(null);
                  }}
                >
                  <img
                    src={`/media/blog/${encodeURIComponent(media.id)}/480`}
                    alt={media.alt || "Untitled image"}
                  />
                </button>
              ))
            ) : (
              <p>The library is empty.</p>
            )}
          </div>
        </Dialog>
      )}
    </div>
  );
}
