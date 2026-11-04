# Kionga frontend

The Kionga frontend uses Next.js App Router, React and TypeScript. It provides
the platform console, developer workspaces, API reference and engineering blog.

- `app/`: public, authenticated console, login and studio routes.
- `components/layout/`: shared navigation and page layouts.
- `components/ui/`: Material icons, native selects and numbered code viewers/editors.
- `components/console/`: permission-aware resource search.
- `components/pages/`: public pages and the authenticated console shell.
- `features/`: operational screens, graph editors, Langflow and shared forms.
- `components/landing/`: animated project flow and interactive local workspace samples.
- `components/blog/`: searchable listing and validated React article rendering.
- `components/studio/`: editorial workflows and the direct Tiptap editor.
- `hooks/`, `lib/`: cancellable API loading, types and editorial conversion.
- `public/`: self-hosted fonts, images and licensed vendor assets.

## Build and run

From the platform root:

```sh
make run                         # installs/builds frontend, then starts Go on :8080
npm run build:frontend           # build only
npm run type-check
npm run test:ui                  # run after building
```

For frontend development against the local stack, point Next.js at the configured
gateway so existing service connections, authentication and workspace settings
remain available:

```sh
KIONGA_GATEWAY_URL=http://127.0.0.1:8080 npm run dev --prefix frontend -- --port 3001
```

To see the agent teams console with canned data and no stack, run
`node frontend/tests/fixtures/teams-mock-gateway.mjs 18917`, set
`KIONGA_GATEWAY_URL=http://127.0.0.1:18917` for the dev server and open it through
`http://localhost`; Next.js refuses development assets on `127.0.0.1`.

Use the stack's published gateway port if it differs from `8080`. `make run`
starts a separate gateway with its own configuration; it does not start or
discover Compose services automatically. Langflow requires the local Compose
profile and a configured upstream, or Kubernetes workspace discovery.

The Next dev server proxies API/auth/media/workspace requests to
`KIONGA_GATEWAY_URL` (defaults to `http://127.0.0.1:8080`). Production exports static
assets into `go/cmd/gateway/frontend-dist/`; Go embeds them. `make build` and the
root-context `Dockerfile.gateway` build the frontend first. Never edit exported HTML.

## Routing and ownership

Public routes include `/`, `/login`, `/api-docs`, `/blog` and
`/blog/read?slug=…`. Authenticated routes include `/console`, individual screens
under `/console/<screen>`, `/workspace` and studio pages under `/studio`.
Project and resource context use query parameters. Retired `.html` URLs are
unavailable. Next.js owns page navigation and React owns all application state,
forms, dialogs, graph canvases and editor workflows.

The Go gateway remains authoritative for authentication, API authorization and
workspace isolation. Frontend routing does not intercept API, auth, media or
workspace proxy endpoints. Effects cancel requests and release subscriptions
when a page unmounts. Unsaved editors protect navigation and browser exit.

## Article content and publishing

The Tiptap editor reads and writes the gateway's article block schema. The
editorial API manages posts, media references, permissions, revisions, autosave
recovery, conflict detection and publishing actions. Dedicated controls support
images, tables, embeds, callouts, quotes and nested lists.

Content serialization preserves block payloads, rich text, media references and
list hierarchy across editing and revision operations.

## Validation

Run `npm test --prefix frontend` for React workflow, request, security and content
round-trip tests. Run `npm run type-check` and `npm run build:frontend` before
browser acceptance with `npm run test:e2e` against a configured stack.

Run `npm run test:core` from the platform root after building the frontend to
verify workspace launch and backend routing through an isolated gateway. The
suite uses temporary state and a Langflow test upstream; Go handles real
authentication, API requests and workspace proxying. The gateway continues to
own workspace lifecycle, isolation, permissions, pipeline execution and storage.

## Product and publication boundaries

The product landing page contains no editorial feed or studio navigation.
Publishing remains an adopter-facing console capability. Reader pages use a
separate publication layout. Set `NEXT_PUBLIC_PUBLICATION_URL` at build time to
send console/studio reader links to the organization's publication origin.
See [Publications](../docs/publications.md) for reader-only hosting and editorial
ownership. API documentation uses bundled Swagger UI with the gateway's OpenAPI
contract.

## Operational controls and integrations

Graph editing uses the shared React Flow canvas for panning, zooming, dragging
and port connections. Agent node rules remain backed by the component registry
and graph validation APIs. Pipeline node inspection queries logs by run and node;
checkpoint resumes submit the existing preflight and saved-output reuse contract.

Storage paths link into the bucket browser with addressable prefix/object state.
Resource facts separate collections and source from scalar records. Native select
pickers receive explicit application styling through `appearance: base-select`
in supporting browsers, retaining platform form and keyboard behavior.

See [Access rules](../docs/access-control.md) for policy evaluation and propagation
and [GitHub connections](../docs/github-integration.md) for account encryption,
App setup, repository import and its boundaries.
