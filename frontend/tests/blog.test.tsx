import { dom } from "./setup-dom";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { render, cleanup, waitFor, fireEvent } from "@testing-library/react";
import { BlogIndex } from "../components/blog/BlogIndex";
import { BlogArticle } from "../components/blog/BlogArticle";
import { isGistURL } from "../components/blog/ArticleBlocks";
const payload = '<img src=x onerror="window.pwned=1">';
const post = (overrides = {}) => ({
  id: "p1",
  slug: "gateway",
  title: "Gateway",
  summary: "How it works",
  author: "Ana",
  tags: ["Go", "Security"],
  created_at: "2026-01-01T00:00:00Z",
  reading_minutes: 4,
  ...overrides,
});
function setup(url: string, data: any, ok = true) {
  history.replaceState(null, "", url);
  globalThis.fetch = async () =>
    ({
      ok,
      status: ok ? 200 : 404,
      json: async () => data,
      text: async () => JSON.stringify(data),
    }) as unknown as Response;
  return dom;
}
afterEach(cleanup);
test("React article escapes metadata and renders validated content blocks", async () => {
  const dom = setup(
    "http://localhost/blog/read?slug=gateway",
    post({
      title: payload,
      summary: payload,
      author: payload,
      tags: [payload],
      blocks: [{ type: "paragraph", data: { text: "Body <b>bold</b>" } }],
    }),
  );
  const view = render(<BlogArticle />);
  await waitFor(() =>
    assert.ok(view.container.querySelector("#article-content strong")),
  );
  assert.equal(view.container.querySelector("#article-header img"), null);
  assert.equal(view.container.querySelector("h1")?.textContent, payload);
  assert.equal(
    view.container.querySelector(".article-byline a")?.getAttribute("href"),
    `/blog?author=${encodeURIComponent(payload)}`,
  );
  assert.equal((dom.window as any).pwned, undefined);
});
test("missing articles offer a link back to the blog", async () => {
  setup("http://localhost/blog/read?slug=missing", {}, false);
  const view = render(<BlogArticle />);
  await waitFor(() =>
    assert.match(view.container.textContent || "", /Article not found/),
  );
  assert.equal(
    view.getByText("Browse all articles").getAttribute("href"),
    "/blog",
  );
});
test("blog filters by author, tag, search and updates the URL", async () => {
  setup("http://localhost/blog?author=Ana", {
    items: [
      post(),
      post({
        id: "p2",
        slug: "s3",
        title: "Mounting S3",
        summary: "FUSE notes",
        author: "Ben",
        tags: ["Storage"],
      }),
      post({ id: "p3", title: payload }),
    ],
    site: { title: "Custom headline", description: "Desc" },
  });
  const view = render(<BlogIndex />);
  await waitFor(() =>
    assert.equal(view.container.querySelectorAll("article").length, 2),
  );
  assert.equal(
    view.container.querySelector("h1")?.textContent,
    "Custom headline",
  );
  assert.equal(view.container.querySelector("article img"), null);
  fireEvent.click(view.getByText("All authors"));
  assert.equal(view.container.querySelectorAll("article").length, 3);
  fireEvent.click(view.getByRole("button", { name: "Storage" }));
  assert.equal(view.container.querySelectorAll("article").length, 1);
  assert.match(location.search, /tag=Storage/);
  fireEvent.click(view.getByRole("button", { name: "All topics" }));
  fireEvent.change(view.getByRole("searchbox"), { target: { value: "fuse" } });
  await waitFor(() =>
    assert.equal(view.container.querySelectorAll("article").length, 1),
  );
});
test("article blocks preserve checklists and safe GitHub gist links without frame injection", async () => {
  setup(
    "http://localhost/blog/read?slug=blocks",
    post({
      blocks: [
        {
          type: "list",
          data: {
            style: "checklist",
            items: [
              { content: "Verified", meta: { checked: true }, items: [] },
            ],
          },
        },
        {
          type: "embed",
          data: {
            service: "github",
            source: "https://gist.github.com/test/abcdef",
            caption: "Example code",
          },
        },
        {
          type: "embed",
          data: {
            service: "youtube",
            embed: "https://evil.example/embed",
            source: "https://evil.example",
            caption: "",
          },
        },
      ],
    }),
  );
  const view = render(<BlogArticle />);
  await waitFor(() =>
    assert.ok(
      view.container.querySelector('#article-content input[type="checkbox"]'),
    ),
  );
  assert.equal(
    (view.container.querySelector("#article-content input") as HTMLInputElement)
      .checked,
    true,
  );
  assert.equal(
    view.getByText("View code on GitHub").getAttribute("href"),
    "https://gist.github.com/test/abcdef",
  );
  assert.equal(view.container.querySelector("iframe"), null);
});

test("only the exact gist host counts as a GitHub gist", () => {
  assert.equal(isGistURL("https://gist.github.com/test/abcdef"), true);
  for (const source of ["https://gistXgithub.com/a", "http://gist.github.com/a", "https://gist.github.com.evil.test/a", "https://evil.test/gist.github.com/a", "not a url", undefined]) {
    assert.equal(isGistURL(source), false, String(source));
  }
});
