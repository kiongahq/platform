# Engineering blog

The public blog lives at `/blogs.html` (index with search, tag filter and
author pages via `/blogs.html?author=<name>`) and `/blog.html?slug=<slug>`
(article). The landing page shows the three newest posts. Writing, review and
publishing happen in the **editorial workspace** at `/editorial.html`, which is
separate from the ML console. Design rationale:
[Decision: blog editor and editorial identity](../reference/decisions/blog-editor.md).

## Roles

Editorial roles are separate from ML platform roles. Being an ML administrator
does **not** let you publish.

| Role | Can |
| --- | --- |
| Author | Create posts, edit own drafts, upload images, submit for review, withdraw |
| Editor | Everything an author can, on any post; request changes, approve & publish, schedule, unpublish, archive, restore |
| Editorial admin | Everything an editor can; add/suspend/remove members, change roles, blog settings |

## For authors

1. Open `/editorial.html` (or **Engineering blog → Open editorial workspace**
   in the console) and sign in with your normal account.
2. **New post.** Type a title (the slug follows it until you edit the slug),
   a one-line summary, then write. Press **Tab** or **+** for headings, lists
   (bulleted, numbered, checklist), quotes, code, tables, callouts, dividers
   and images. Select text for bold, italic, links, inline code and highlight.
   Paste a YouTube, Vimeo or GitHub gist URL to embed it.
3. **Images:** choose a file, drag it onto the image block or the editor, or
   paste it. A progress bar shows the upload. Fill in **Alt text** (required
   to publish), an optional caption and the alignment (centered, wide, full).
4. **Saving:** the status next to the post badge shows `Saving…`,
   `Saved hh:mm`, or `Offline — kept locally`. Changes autosave; Ctrl/⌘ S saves
   now. If the browser or network fails, reopening the post offers **Restore
   my changes**. If someone else saved in the meantime you choose: keep yours
   (saved on top) or load theirs.
5. **Preview** shows the post exactly as readers will see it (same server
   renderer), at desktop width or a 390 px phone width.
6. **History** lists every save. Select two revisions to compare; **Restore**
   creates a new revision with the old content.
7. Set tags, a cover image (with alt text) and optional SEO title/description;
   the social card preview updates as you type. **Focus** hides everything but
   the canvas (Esc exits). The undo/redo arrows step through block changes.
8. **Submit for review.** The post becomes read-only for you until an editor
   publishes it or requests changes.

Markdown is optional: **Import Markdown** appends converted blocks, and
**Export Markdown** downloads the post.

## For editors

- **Reviews** lists posts waiting for you. Open one, read it in Preview, then
  **Approve & publish**, **Schedule** (pick the publish date/time first; the
  scheduler publishes it within about 20 seconds of that time) or **Request
  changes** (back to draft).
- **Unpublish** removes a post from the public site and returns it to draft;
  **Archive** hides it; **Restore from archive** brings it back as a draft.
- Publishing is blocked until every image and the cover have alt text.
- Editors can fix typos on published posts; saves go live immediately and are
  recorded as revisions.

## Operator notes

- **Bootstrap:** the first time, the local bootstrap administrator (or an ML
  admin with OIDC) opens `/editorial.html` and clicks **Become the first
  editorial admin**. This is audited and only possible while no active
  editorial admin exists; the last editorial admin cannot be removed, demoted
  or suspended. Then add people under **Authors** by their platform identity
  (local username or OIDC subject).
- **Session:** cookie `kionga_editorial`, HttpOnly, SameSite=Strict, 4 hours,
  membership re-checked on every request.
- **Media storage:** bucket `kionga-blog-media` (created by
  `deploy/minio-init.py`). The gateway selects the backend from environment:

  | Variable | Default | Meaning |
  | --- | --- | --- |
  | `KIONGA_BLOG_MEDIA_BACKEND` | `s3` if an endpoint is set, else `fs` | `s3` or `fs` |
  | `KIONGA_BLOG_MEDIA_S3_ENDPOINT` | none | e.g. `http://minio:9000` |
  | `KIONGA_BLOG_MEDIA_S3_ACCESS_KEY`, `_SECRET_KEY`, `_REGION` | none, none, `us-east-1` | credentials |
  | `KIONGA_BLOG_MEDIA_BUCKET` | `kionga-blog-media` | bucket |
  | `KIONGA_BLOG_MEDIA_DIR` | `data/blog-media` | filesystem backend root |

  Compose sets the S3 variables for the gateway. Objects are stored as
  `media/<media id>/<480|960|1600>`.
- **Limits:** 10 MB per upload; JPEG, PNG, WebP, GIF (detected from bytes);
  at most 12000 px per side and 50 megapixels; EXIF and other metadata removed
  (orientation applied first); animated GIFs keep their first frame.
  Unattached uploads are deleted after 24 hours.
- **Scheduler:** the publisher honors `KIONGA_SCHEDULER=off` like the pipeline
  scheduler. A lease keeps one replica active.
- **Metrics:** `kionga_editorial_saves_total{result}`,
  `kionga_editorial_transitions_total{action,result}`,
  `kionga_editorial_media_uploads_total{result}`,
  `kionga_editorial_scheduled_publishes_total` on the gateway metrics port.
- **Records:** posts (`editorial_post`), revisions (`blog_revision`), media
  (`blog_media`), members (`editorial_member`), sessions (`editorial_session`,
  token hashes only) and slugs (`editorial_slug`) are generic documents in
  PostgreSQL `platform_resources` (or the JSON file store), so they inherit
  tenancy, backup and auditing. Back up the media bucket with PostgreSQL.
- **Migration:** on startup, Markdown posts from the previous blog are
  converted to blocks once (marker kind `editorial_migration`), keeping slugs,
  authors, status and dates; the original Markdown stays in `legacy_markdown`.
  The old `/api/v1/admin/blogs` endpoints are removed.
