import "./setup-dom";
import { test } from "node:test";
import assert from "node:assert/strict";
import { consoleViewAllowed } from "../components/pages/ConsolePage";
import type { Identity } from "../lib/types";

const admin: Identity = { subject: "admin", roles: ["admin"], services: [], mode: "local", permissions: {} };
const user: Identity = { subject: "u", roles: ["user"], services: ["applications"], mode: "local", permissions: {} };

test("Publishing is hidden unless the install enables blog authoring", () => {
  assert.equal(consoleViewAllowed("blogs", admin, false), false);
  assert.equal(consoleViewAllowed("blogs", user, false), false);
  assert.equal(consoleViewAllowed("blogs", admin, true), true);
  assert.equal(consoleViewAllowed("blogs", user, true), true);
});

test("other views keep their service gating either way", () => {
  for (const authoring of [false, true]) {
    assert.equal(consoleViewAllowed("applications", user, authoring), true);
    assert.equal(consoleViewAllowed("pipelines", user, authoring), false);
    assert.equal(consoleViewAllowed("access", user, authoring), false);
    assert.equal(consoleViewAllowed("access", admin, authoring), true);
    assert.equal(consoleViewAllowed("profile", user, authoring), true);
  }
});
