# Publications

Publishing is an optional platform capability. An adopting organization can use
the editorial studio, article API and reader interface to document its own
engineering practices. Its articles are not product marketing content and do not
appear on the product landing page.

Kionga's engineering publication must have its own domain, editorial identity
and content. It is not a shared article feed for
platform installations. Each installation controls its publication settings,
editorial membership, revisions, media and publishing workflow.

## Reader domain

Set `NEXT_PUBLIC_PUBLICATION_URL` to the publication's HTTP or HTTPS origin before
building the frontend. Console and studio reader links use this origin. Without
it, local development retains `/blog` and `/blog/read?slug=…` reader routes.

For container builds, pass `--build-arg NEXT_PUBLIC_PUBLICATION_URL=…` to
`Dockerfile.gateway`; Compose builds must forward the same build argument. The
reader has dedicated publication navigation; the product landing page links to
neither the reader nor the editorial studio.

Use `config/publication.Caddyfile` as an optional reader-only virtual host. Set
`KIONGA_PUBLICATION_DOMAIN` to the actual domain and
`KIONGA_PUBLICATION_UPSTREAM` to the gateway that owns that publication's data.
Configure DNS and TLS at the edge. Keep `/studio`, authentication and publishing
writes on the authenticated platform origin. The example virtual host exposes
reader pages, public article reads, media and static assets only.

For Kionga's own engineering publication, use a separate installation and data
store. Adopters connect their own installation; they must not point their reader
host at Kionga's editorial data. Existing article APIs and workspace services are
unchanged by reader hosting.

## API documentation

`/api-docs` is a dedicated Swagger UI surface backed by `/api/openapi.json`.
Swagger assets are bundled with the frontend. Online specification validation
and URL-based configuration overrides are disabled. Authentication and API
permissions remain enforced by the gateway. Interactive requests run only when
a reader explicitly uses Swagger's request controls.
