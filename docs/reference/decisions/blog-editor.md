# Decision: blog editor, canonical content and editorial identity

Status: accepted (Phase 8). Scope: the engineering blog and its editorial
workspace (`/editorial.html`).

## 1. Editor: Editor.js

**Chosen:** [Editor.js](https://editorjs.io) 2.31.7 with the official header,
list, quote, code, delimiter, embed, table, inline-code, marker and warning
tools, vendored as the upstream UMD builds under
`go/cmd/gateway/web/vendor/editorjs/` with their licenses.

Why:

- **Vanilla, no bundler.** The console and public site are plain scripts
  embedded with `go:embed`. Editor.js and every tool ship a browser-ready UMD
  file that registers one global; nothing needs a build step.
- **Block JSON output.** Editor.js saves `{blocks: [{id, type, data}]}`. That
  is the canonical content (section 2): structured, diffable per block, and
  renderable on the server without parsing HTML from the browser.
- **Stable block ids** make revision comparison deterministic (blocks are
  matched by id, not by position).

Alternatives considered:

| Option | Why not |
| --- | --- |
| Tiptap | Distributed as ES modules over ProseMirror; a production setup needs a bundler (or a large import map of ProseMirror packages). Output is ProseMirror JSON or HTML, which pushes HTML sanitizing of the whole document to the server anyway. |
| Lexical | Same bundler requirement (React-first packaging, ESM only); vanilla use is possible but undocumented for our no-build setup. |
| Markdown textarea (previous) | Required authors to know Markdown, no media workflow, no structured diff. Kept only as optional import/export. |
| Quill | Delta format is not block-structured; image and embed handling would still need custom tools. |

`@editorjs/image` was evaluated and **not** used: it has no alt-text field, no
alignment, no upload progress, and stores a free URL. Publishing requires alt
text and images must come only from our media endpoint, so the workspace ships
a small local block tool (`editorial/image-tool.js`) that stores a `media_id`.
Editor.js has no undo/redo module; block-level undo/redo is a snapshot history
in `editorial/state.js`, and text-level undo is the browser's own.

### Supply chain

`scripts/vendor-manifest.py` compares every vendored file with the SHA-256 that
the jsDelivr package API publishes for the same pinned npm version (an
independent channel from the download) and writes `MANIFEST.json` with the
SHA-384 used for Subresource Integrity. `editorial.html` loads each script with
`integrity`; `blog.html` pins highlight.js 11.11.1 ES modules through an import
map `integrity` table. `tests/ui/vendor-integrity.test.cjs` recomputes the hashes
on every gate run and fails if a page references a vendor file without the
pinned hash. Note: `@editorjs/editorjs` currently has its npm `latest` dist-tag
pointing at a `0.0.0` preview of a rewrite; 2.31.7 (the `next` tag) is the
latest 2.x release and is what we pin.

## 2. Canonical content: blocks, rendered on the server

- Stored: block JSON (`editorial_post.blocks`). Markdown is never required.
- On save, `editorial.NormalizeBlocks` validates every block type, clamps enum
  values and rebuilds inline HTML from an allowlist (`b, i, a[href], code,
  mark, br, s, u`). Links get `rel="noopener noreferrer nofollow"`; only http(s),
  mailto, site-relative and `#` links survive.
- On read, `editorial.Render` turns blocks into HTML and passes the result
  through a **bluemonday** policy narrowed to exactly what the renderer emits:
  images only from `/media/blog/<id>/<480|960|1600>`, iframes only from
  `youtube-nocookie.com/embed/<id>` and `player.vimeo.com/video/<id>` with a
  `sandbox`, GitHub gists as plain links, no `style`, no `data:`. The embed URL
  a client sends is ignored; it is rebuilt from the validated source URL.
- The public page inserts only `rendered_html` as markup; every other field
  is escaped in the browser.
- Legacy Markdown posts (including the seed) are converted to blocks once at
  startup (`MigrateLegacy`), keep id, slug, author, status and dates, and keep
  the original in `legacy_markdown`.

## 3. Identity: same verified identity, separate editorial roles and session

**Chosen:** editorial access is a separate membership (document kind
`editorial_member`, roles `author`, `editor`, `editorial_admin`) on top of the
same verified platform identity (local account or OIDC), with its own cookie.

- `POST /api/v1/editorial/session` mints `kionga_editorial` (Path=/,
  HttpOnly, SameSite=Strict, Secure behind TLS/OIDC, 4 h TTL) **only for an
  active member**. The server stores only the SHA-256 of the token.
- Every `/api/v1/editorial/*` request needs that session (403 otherwise), the
  membership is re-read on every request (removal or suspension is immediate),
  and the session subject must equal the signed-in platform subject, so a
  leftover cookie cannot be used by another person on a shared browser.
  Mutations with `Sec-Fetch-Site: cross-site` are refused on top of SameSite.
- **ML admins have no publishing rights** unless they are members. The gateway
  RBAC lets every human principal reach `/api/v1/editorial/*` and lets the
  membership decide; service identities and API tokens never reach it.
- **Bootstrap:** while no active `editorial_admin` exists, the local bootstrap
  administrator (local mode) or an ML admin may grant themselves the first
  `editorial_admin` (`POST /api/v1/editorial/bootstrap`, audited, serialized by a
  create-only claim document). The last active editorial admin cannot be
  demoted, suspended or removed, so bootstrap does not reopen.

Why not reuse ML roles: publishing to a public site is a different trust
domain from operating ML infrastructure. Coupling them would make every
platform operator a publisher and every writer a platform user with admin
reach. A separate session also keeps the blast radius of a stolen console
cookie away from the public site.

Alternatives considered: an OIDC group claim (`kionga-editors`) — rejected for
now because local mode has no groups and memberships need per-person roles that
editorial admins manage themselves; an editorial role inside `UserAccess` —
rejected because it would put publishing back under ML administrators.

## 4. Workflow, revisions, scheduling, media

- `draft → in_review → scheduled | published`; `unpublish → draft`; `archive`
  and `restore`. Authors create, edit their own drafts and submit/withdraw;
  editors also review, request changes, publish, schedule, unpublish, archive;
  editorial admins also manage members and settings. Publishing requires a
  title, summary, 20+ words and alt text on every image and the cover.
- Every content save writes an immutable `blog_revision`; unchanged autosaves
  are no-ops. `PUT` takes `base_revision`; a stale base returns 409 with the
  latest post. Restore creates a new revision and keeps the post's slug.
- Scheduled publishing reuses the scheduler pattern: a lease document picks one
  replica, and the per-post row lock plus a status re-check make double
  publication impossible even if two replicas both think they lead.
- Media: multipart upload ≤ 10 MB, type sniffed from bytes (JPEG/PNG/WebP/GIF;
  filename and client type ignored), dimensions bounded before decoding, EXIF
  orientation applied, then re-encoded (which drops all metadata) into
  480/960/1600 px variants. Bytes go to a `MediaStore` (S3/MinIO with SigV4
  presigned requests, or the filesystem for local dev/tests). Uploads are
  `pending` until a save references them (`attached`); pending items older than
  24 h are swept. `/media/blog/{id}/{variant}` is public only while the media is
  attached to a published post that still references it.
