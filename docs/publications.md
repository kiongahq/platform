# Publications

Publishing is an optional platform capability, off by default. Set
`KIONGA_BLOG_AUTHORING_ENABLED=true` on the gateway to turn it on; while it is
off the studio, the console Publishing page, the editorial API, the public
article API (`/api/v1/blogs`) and blog media answer 404 and the console hides
the navigation. An adopting organization can use
the editorial studio and article API to document its own engineering practices.
The install serves no reader pages and no landing page: articles are rendered
by a separate website that reads the public article API.

Kionga's engineering publication must have its own domain, editorial identity
and content. It is not a shared article feed for
platform installations. Each installation controls its publication settings,
editorial membership, revisions, media and publishing workflow.

## Reader domain

The console and studio link to published articles on the website configured
by `KIONGA_WEBSITE_URL` (read at runtime through `GET /api/v1/site`), or on
`NEXT_PUBLIC_PUBLICATION_URL` when a frontend build sets it. With neither set,
the links are hidden. Article paths on that site are `/blog` and
`/blog/read?slug=…`.

Use `config/publication.Caddyfile` as an optional public host for the article
API and media only. Set `KIONGA_PUBLICATION_DOMAIN` to the actual domain and
`KIONGA_PUBLICATION_UPSTREAM` to the gateway that owns that publication's data.
Keep `/studio`, authentication and publishing writes on the authenticated
platform origin.

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
