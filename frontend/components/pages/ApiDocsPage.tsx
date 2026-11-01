"use client";
import dynamic from "next/dynamic";
import Link from "next/link";

const SwaggerUI = dynamic(() => import("swagger-ui-react"), {
  ssr: false,
  loading: () => <p role="status">Loading Swagger UI…</p>,
});

/** A dedicated documentation surface backed by the gateway's OpenAPI contract. */
export function ApiDocsPage() {
  return (
    <main className="swagger-reference" id="main">
      <header className="swagger-reference-header">
        <div>
          <h1>Kionga API reference</h1>
          <p>Explore the platform API with Swagger UI.</p>
        </div>
        <nav aria-label="API documentation">
          <Link href="/console">Console</Link>
          <a href="/api/openapi.json">OpenAPI JSON</a>
        </nav>
      </header>
      <SwaggerUI
        url="/api/openapi.json"
        plugins={[{ wrapComponents: { onlineValidatorBadge: () => () => null } }]}
        filter
        displayRequestDuration
        persistAuthorization={false}
        queryConfigEnabled={false}
      />
    </main>
  );
}
