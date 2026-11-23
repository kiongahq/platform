import "./setup-dom";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { render, cleanup } from "@testing-library/react";
import { ArticleBlocks, isGistURL } from "../components/studio/ArticleBlocks";
afterEach(cleanup);

// The studio preview renders article blocks; the public reader moved to the
// website, so the block rules are tested here directly.
test("article blocks preserve checklists and safe GitHub gist links without frame injection", () => {
  const view = render(
    <ArticleBlocks
      blocks={[
        { type: "list", data: { style: "checklist", items: [{ content: "Verified", meta: { checked: true }, items: [] }] } },
        { type: "embed", data: { service: "github", source: "https://gist.github.com/test/abcdef", caption: "Example code" } },
        { type: "embed", data: { service: "youtube", embed: "https://evil.example/embed", source: "https://evil.example", caption: "" } },
      ] as any}
    />,
  );
  assert.equal((view.container.querySelector('input[type="checkbox"]') as HTMLInputElement).checked, true);
  assert.equal(view.getByText("View code on GitHub").getAttribute("href"), "https://gist.github.com/test/abcdef");
  assert.equal(view.container.querySelector("iframe"), null);
});

test("only the exact gist host counts as a GitHub gist", () => {
  assert.equal(isGistURL("https://gist.github.com/test/abcdef"), true);
  for (const source of ["https://gistXgithub.com/a", "http://gist.github.com/a", "https://gist.github.com.evil.test/a", "https://evil.test/gist.github.com/a", "not a url", undefined]) {
    assert.equal(isGistURL(source), false, String(source));
  }
});
