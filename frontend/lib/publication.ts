/** Optional reader origin for deployments that serve publishing on a separate domain. */
export function publicationHref(path = "/blog") {
  const origin = process.env.NEXT_PUBLIC_PUBLICATION_URL;
  if (!origin) return path;
  const target = new URL(origin);
  if (!["https:", "http:"].includes(target.protocol))
    throw new Error("Publication URL must use HTTP or HTTPS");
  return new URL(path, target.origin).href;
}
