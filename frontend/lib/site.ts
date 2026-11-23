"use client";
import { useEffect, useState } from "react";

/** Validates the website link from /api/v1/site: absolute http(s) only. */
export function safeWebsiteURL(value: unknown): string {
  if (typeof value !== "string" || !value) return "";
  try {
    const url = new URL(value);
    if (!["https:", "http:"].includes(url.protocol) || url.username || url.password) return "";
    return url.href;
  } catch {
    return "";
  }
}

/**
 * The optional link to the Kionga website (KIONGA_WEBSITE_URL on the
 * gateway). Empty means hidden.
 */
export function useWebsiteURL() {
  const [url, setURL] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/v1/site", { signal: controller.signal, headers: { Accept: "application/json" } })
      .then((response) => (response.ok ? response.json() : undefined))
      .then((value) => setURL(safeWebsiteURL(value?.website_url)))
      .catch(() => {});
    return () => controller.abort();
  }, []);
  return url;
}

/** A path on the website, or "" when no website is configured. */
export function websiteHref(website: string, path: string) {
  if (!website) return "";
  return new URL(path, website).href;
}
