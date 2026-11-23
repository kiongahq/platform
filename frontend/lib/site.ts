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

export type SiteSettings = { websiteURL: string; blogAuthoring: boolean };

/**
 * Install settings from /api/v1/site: the optional link to the Kionga website
 * (KIONGA_WEBSITE_URL; empty means hidden) and whether blog authoring is on
 * (KIONGA_BLOG_AUTHORING_ENABLED; off by default).
 */
export function useSite(): SiteSettings {
  const [site, setSite] = useState<SiteSettings>({ websiteURL: "", blogAuthoring: false });
  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/v1/site", { signal: controller.signal, headers: { Accept: "application/json" } })
      .then((response) => (response.ok ? response.json() : undefined))
      .then((value) => setSite({ websiteURL: safeWebsiteURL(value?.website_url), blogAuthoring: value?.blog_authoring === true }))
      .catch(() => {});
    return () => controller.abort();
  }, []);
  return site;
}

/** The optional link to the Kionga website; empty means hidden. */
export function useWebsiteURL() {
  return useSite().websiteURL;
}

/** A path on the website, or "" when no website is configured. */
export function websiteHref(website: string, path: string) {
  if (!website) return "";
  return new URL(path, website).href;
}
