import type { Block } from "./editor-blocks";
export type Member = {
  subject: string;
  display_name: string;
  email?: string;
  role: "author" | "editor" | "editorial_admin";
  disabled?: boolean;
  posts?: number;
};
export type Draft = {
  title: string;
  summary: string;
  slug: string;
  tags: string[];
  blocks: Block[];
  cover: { media_id: string; alt: string } | null;
  seo: { title?: string; description?: string };
  editor_version?: string;
};
export type EditorialPost = Draft & {
  id: string;
  revision: number;
  status: string;
  can_edit: boolean;
  actions: string[];
  author: string;
  created_at: string;
  updated_at: string;
  updated_by?: string;
  publish_at?: string;
  published_at?: string;
};
export type Media = {
  id: string;
  alt: string;
  caption: string;
  attribution: string;
  width: number;
  height: number;
  status: string;
  created_at: string;
};
export type Session = {
  member: Member;
  limits?: {
    session_hours?: number;
    max_upload_bytes?: number;
    variant_widths?: number[];
  };
  media_backend?: string;
};
export const roles: Record<string, string> = {
  author: "Author",
  editor: "Editor",
  editorial_admin: "Editorial admin",
};
export const statuses: Record<string, string> = {
  draft: "Draft",
  in_review: "In review",
  scheduled: "Scheduled",
  published: "Published",
  archived: "Archived",
};
export const actions: Record<string, string> = {
  submit: "Submit for review",
  withdraw: "Withdraw from review",
  request_changes: "Request changes",
  publish: "Approve & publish",
  schedule: "Schedule",
  unpublish: "Unpublish",
  archive: "Archive",
  restore: "Restore from archive",
};
export class EditorialError extends Error {
  constructor(
    public status: number,
    public body: Record<string, any>,
  ) {
    super(body.message || `Request failed (${status})`);
  }
  get latest(): EditorialPost | undefined {
    return this.body.latest;
  }
}
export async function editorial<T = any>(
  path: string,
  options: { method?: string; body?: unknown; signal?: AbortSignal } = {},
): Promise<T> {
  const response = await fetch(path, {
    method: options.method || "GET",
    credentials: "same-origin",
    signal: options.signal,
    headers:
      options.body === undefined
        ? { Accept: "application/json" }
        : { "Content-Type": "application/json", Accept: "application/json" },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (response.status === 401 && typeof location !== "undefined")
    location.assign(
      `/auth/login?return_to=${encodeURIComponent(location.pathname + location.search)}`,
    );
  if (response.status === 204) return null as T;
  if (response.status === 401 && typeof window !== "undefined") window.dispatchEvent(new window.Event("kionga:session-expired"));
  const data = await response
    .json()
    .catch(() => ({ message: "The server returned an unreadable response." }));
  if (!response.ok) throw new EditorialError(response.status, data);
  return data;
}
export function uploadMedia(
  file: File,
  options: {
    alt?: string;
    caption?: string;
    attribution?: string;
    onProgress?: (progress: number) => void;
  } = {},
): Promise<{ file: { media_id: string; url: string }; media: Media }> {
  return new Promise((resolve, reject) => {
    const form = new FormData();
    form.append("file", file);
    form.append("alt", options.alt || "");
    form.append("caption", options.caption || "");
    form.append("attribution", options.attribution || "");
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/v1/editorial/media");
    xhr.withCredentials = true;
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable)
        options.onProgress?.(Math.round((event.loaded / event.total) * 100));
    };
    xhr.onload = () => {
      let data;
      try {
        data = JSON.parse(xhr.responseText);
      } catch {
        data = { message: "The upload response could not be read." };
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        options.onProgress?.(100);
        resolve(data);
      } else reject(new EditorialError(xhr.status, data));
    };
    xhr.onerror = () =>
      reject(
        new Error("Network error while uploading. Try again when connected."),
      );
    xhr.send(form);
  });
}
export const editURL = (id: string) =>
  `/studio/edit?post=${encodeURIComponent(id)}`;
export const slugify = (value: string) =>
  value
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 80)
    .replace(/-+$/g, "");
export const emptyDraft = (): Draft => ({
  title: "",
  summary: "",
  slug: "",
  tags: [],
  blocks: [],
  cover: null,
  seo: {},
  editor_version: "kionga-tiptap-3",
});
export const fingerprint = (value: Partial<Draft> | null | undefined) =>
  value
    ? JSON.stringify([
        value.title || "",
        value.summary || "",
        value.slug || "",
        value.tags || [],
        (value.blocks || []).map((block) => [block.id, block.type, block.data]),
        value.cover?.media_id,
        value.cover?.alt,
        value.seo?.title || "",
        value.seo?.description || "",
      ])
    : "";
export const recoveryPrefix = "kionga.editorial.recovery.";
export type Recovery = {
  snapshot: Draft;
  base_revision: number;
  saved_at: string;
};
export function readRecovery(key: string): Recovery | null {
  try {
    return JSON.parse(localStorage.getItem(recoveryPrefix + key) || "null");
  } catch {
    return null;
  }
}
export function clearRecovery(key: string) {
  try {
    localStorage.removeItem(recoveryPrefix + key);
  } catch {}
}
export function saveRecovery(key: string, value: Recovery) {
  try {
    localStorage.setItem(recoveryPrefix + key, JSON.stringify(value));
  } catch {}
}
export function wordCount(blocks: Block[]) {
  const strip = (value: unknown) =>
    String(value || "").replace(/<[^>]*>/g, " ");
  const list = (items: any[]): string =>
    (items || [])
      .map((item) =>
        typeof item === "string"
          ? strip(item)
          : `${strip(item.content)} ${list(item.items)}`,
      )
      .join(" ");
  const text = (blocks || [])
    .map((block) => {
      const d = block.data;
      if (block.type === "list") return list(d.items);
      if (block.type === "table")
        return (d.content || []).flat().map(strip).join(" ");
      if (block.type === "code") return d.code || "";
      return [d.text, d.caption, d.title, d.message].map(strip).join(" ");
    })
    .join(" ");
  return text.split(/\s+/).filter(Boolean).length;
}
