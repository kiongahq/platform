import "./setup-dom";
import React from "react";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import { render, cleanup, fireEvent, waitFor } from "@testing-library/react";
import { ResourceSearch } from "../components/console/ResourceSearch";
afterEach(cleanup);
test("resource search observes grants, filters assigned projects and opens the selected resource with its project", async () => {
  const calls: string[] = [];
  let selected: any;
  window.HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  window.HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
  globalThis.fetch = async (path) => {
    calls.push(String(path));
    return new Response(
      JSON.stringify({
        items: String(path).endsWith("/projects")
          ? [
              { id: "p1", name: "Fraud project" },
              { id: "p2", name: "Support project" },
            ]
          : [
              {
                id: "model-1",
                name: "Fraud classifier",
                project_id: "p1",
                version: "v2",
              },
              { id: "model-2", name: "Support classifier", project_id: "p2" },
            ],
      }),
    );
  };
  const view = render(
    <ResourceSearch
      identity={{
        subject: "user",
        roles: ["user"],
        services: ["projects", "models"],
        permissions: {},
        mode: "local",
      }}
      project="p1"
      projects={[
        { id: "p1", name: "Fraud project" },
        { id: "p2", name: "Support project" },
      ]}
      onClose={() => {}}
      onSelect={(...args) => {
        selected = args;
      }}
    />,
  );
  await waitFor(() =>
    assert.equal(
      view
        .getAllByRole("option", { selected: false })
        .filter((node) => node.tagName === "BUTTON").length +
        view
          .getAllByRole("option", { selected: true })
          .filter((node) => node.tagName === "BUTTON").length,
      2,
    ),
  );
  assert.deepEqual(calls.sort(), ["/api/v1/models", "/api/v1/projects"]);
  fireEvent.change(
    view.getByRole("combobox", { name: "Search by name or resource ID" }),
    { target: { value: "model-1" } },
  );
  fireEvent.keyDown(
    view.getByRole("combobox", { name: "Search by name or resource ID" }),
    { key: "Enter" },
  );
  assert.deepEqual(selected, ["models", "model-1", "p1"]);
  assert.equal(view.queryByText("Support classifier"), null);
});
