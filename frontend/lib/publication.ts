/**
 * Where published articles are read. The install serves no blog pages: the
 * reader lives on the Kionga website (KIONGA_WEBSITE_URL, read at runtime)
 * or on NEXT_PUBLIC_PUBLICATION_URL when a build sets it. Returns "" when
 * neither is configured, so callers hide the link.
 */
export function publicationHref(path = "/blog", website = "") {
  const origin = website || process.env.NEXT_PUBLIC_PUBLICATION_URL || "";
  if (!origin) return "";
  const target = new URL(origin);
  if (!["https:", "http:"].includes(target.protocol))
    throw new Error("Publication URL must use HTTP or HTTPS");
  return new URL(path, target.origin).href;
}
