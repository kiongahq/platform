import test from "node:test";
import assert from "node:assert/strict";
import { normalizeConsolePreferences } from "../lib/preferences";

test("legacy theme choices reset to light without losing other preferences", () => {
  assert.deepEqual(normalizeConsolePreferences({ theme: "dark", compact: true }), {
    theme: "light", themeVersion: 1, compact: true,
  });
});
test("explicit current dark choices persist", () => {
  assert.equal(normalizeConsolePreferences({ theme: "dark", themeVersion: 1 }).theme, "dark");
});
test("missing or invalid preferences default to light", () => {
  for (const value of [null, [], "dark", {}, {theme: "system", themeVersion: 1}]) {
    assert.equal(normalizeConsolePreferences(value).theme, "light");
  }
});
