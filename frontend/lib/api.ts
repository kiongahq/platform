export class APIError extends Error {
  constructor(
    public status: number,
    public body: Record<string, any>,
  ) {
    super(
      body.message || body.error_description || `Request failed (${status})`,
    );
    this.name = "APIError";
  }
  get code() {
    return this.body.error;
  }
  get latest() {
    return this.body.latest;
  }
}
export type RequestOptions = {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  headers?: HeadersInit;
};
export async function request<T = any>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    method: options.method || "GET",
    signal: options.signal,
    headers: {
      Accept: "application/json",
      ...(options.body === undefined
        ? {}
        : { "Content-Type": "application/json" }),
      ...options.headers,
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (response.status === 401 && typeof window !== "undefined") window.dispatchEvent(new window.Event("kionga:session-expired"));
  if (response.status === 204) return null as T;
  const text = await response.text();
  let body: any;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    throw new APIError(response.status, {
      message: "The server returned an unexpected response. Please retry.",
    });
  }
  if (!response.ok) throw new APIError(response.status, body || {});
  return body as T;
}
export const api = <T>(path: string, signal?: AbortSignal) =>
  request<T>(path, { signal });
export const errorMessage = (error: unknown) =>
  error instanceof Error
    ? error.message
    : "The request could not be completed.";
export type Post = {
  id: string;
  slug: string;
  title: string;
  summary: string;
  author: string;
  tags: string[];
  created_at: string;
  published_at?: string;
  reading_minutes: number;
  blocks?: import("./editor-blocks").Block[];
  rendered_html?: string;
  cover?: {
    url: string;
    alt: string;
    srcset?: string;
    width?: number;
    height?: number;
  };
  related?: Post[];
  seo?: { title?: string; description?: string };
};
