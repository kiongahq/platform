import { PHASE_DEVELOPMENT_SERVER } from "next/constants.js";
/** @type {import('next').NextConfig} */
const shared = {
  outputFileTracingRoot: new URL(".", import.meta.url).pathname,
  reactStrictMode: true,
  agentRules: false,
  images: { unoptimized: true },
};
export default (phase) =>
  phase === PHASE_DEVELOPMENT_SERVER
    ? {
        ...shared,
        async rewrites() {
          const gateway =
            process.env.KIONGA_GATEWAY_URL || "http://127.0.0.1:8080";
          return ["api", "auth", "media", "workspaces"].map((prefix) => ({
            source: `/${prefix}/:path*`,
            destination: `${gateway}/${prefix}/:path*`,
          }));
        },
      }
    : { ...shared, output: "export" };
