import "./setup-dom";
import React, { useState } from "react";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import { render, fireEvent, cleanup, waitFor } from "@testing-library/react";
import { formatCode, formatJSON } from "../lib/code-format";
import { CodeBlock } from "../components/ui/CodeBlock";
import { CodeEditor } from "../components/ui/CodeEditor";
import { ActionForm } from "../features/shared/View";
Object.assign(globalThis, { FormData: window.FormData });
afterEach(cleanup);
test("JSON formatting retains large integers, escaped strings and empty collections", () => {
  const original =
    '{"id":9007199254740993,"text":"a\\\"b\\nc","items":[],"meta":{},"ratio":1e-10}';
  const formatted = formatJSON(original);
  assert.match(formatted, /9007199254740993/);
  assert.match(formatted, /1e-10/);
  assert.match(formatted, /"items": \[\]/);
  assert.deepEqual(JSON.parse(formatted), JSON.parse(original));
  assert.throws(() => formatJSON("{bad"));
});
test("YAML formatting preserves comments, anchors, block strings, large integers and multiple documents", () => {
  const source =
    '# project\nbase: &config {image: "python:3.12", retries: 2}\njob: *config\nid: 9007199254740993\nscript: |\n  print("hello")\n---\nname: second\n';
  const formatted = formatCode(source, "yaml");
  for (const text of [
    "# project",
    "&config",
    "*config",
    "9007199254740993",
    'print("hello")',
    "name: second",
  ])
    assert.ok(formatted.includes(text));
  assert.match(formatted, /base: &config\n  image:/);
  assert.throws(() => formatCode("jobs: [broken", "yaml"));
});
test("controlled code formatting updates the actual textarea value without changing submitted data", async () => {
  function Editor() {
    const [value, setValue] = useState('{"cpu":2,"items":[1,2]}');
    return (
      <CodeEditor
        label="Runtime JSON"
        value={value}
        onChange={(event) => setValue(event.target.value)}
      />
    );
  }
  const view = render(<Editor />);
  fireEvent.click(view.getByRole("button", { name: "Format Runtime JSON" }));
  await waitFor(() =>
    assert.match(
      (
        view.getByRole("textbox", {
          name: "Runtime JSON",
        }) as HTMLTextAreaElement
      ).value,
      /\n  "cpu": 2/,
    ),
  );
  assert.equal(
    document.activeElement,
    view.getByRole("textbox", { name: "Runtime JSON" }),
  );
  fireEvent.change(view.getByRole("textbox"), {
    target: { value: "{invalid" },
  });
  fireEvent.click(view.getByRole("button", { name: "Format Runtime JSON" }));
  assert.ok(view.getByRole("alert"));
  assert.equal(
    (view.getByRole("textbox") as HTMLTextAreaElement).value,
    "{invalid",
  );
});
test("uncontrolled JSON forms retain request shape after formatting and native FormData submission", async () => {
  let saved: any;
  const view = render(
    <ActionForm
      fields={[
        {
          name: "config",
          label: "Configuration",
          type: "json",
          value: { cpu: 2 },
        },
      ]}
      onSubmit={async (value) => {
        saved = value;
      }}
    />,
  );
  fireEvent.change(view.getByRole("textbox", { name: "Configuration" }), {
    target: { value: '{"cpu":4,"enabled":true}' },
  });
  fireEvent.click(view.getByRole("button", { name: "Format Configuration" }));
  fireEvent.submit(view.container.querySelector("form")!);
  await waitFor(() => assert.ok(saved));
  assert.deepEqual(saved, { config: { cpu: 4, enabled: true } });
});
test("numbered code stays escaped and copying excludes the line gutter", async () => {
  let copied = "";
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: {
      writeText: async (value: string) => {
        copied = value;
      },
    },
  });
  const source = 'const html = "<img src=x onerror=bad()>";\nreturn html;';
  const view = render(
    <CodeBlock language="javascript" format={false}>
      {source}
    </CodeBlock>,
  );
  assert.equal(view.container.querySelector("img"), null);
  assert.equal(view.container.querySelectorAll(".code-line-number").length, 2);
  fireEvent.click(view.getByRole("button", { name: "Copy code" }));
  await waitFor(() => assert.equal(copied, source));
});
