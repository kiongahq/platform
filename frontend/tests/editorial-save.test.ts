import { dom } from "./setup-dom";
import { test } from "node:test";
import assert from "node:assert/strict";
import { createSaveController } from "../lib/editorial-save";
import {
  emptyDraft,
  EditorialError,
  readRecovery,
  type Draft,
  type EditorialPost,
} from "../lib/editorial";
Object.defineProperty(globalThis, "localStorage", {
  value: dom.window.localStorage,
  configurable: true,
});
const post = (draft: Draft, revision = 1): EditorialPost => ({
  ...draft,
  id: "post-test",
  revision,
  status: "draft",
  can_edit: true,
  actions: ["submit"],
  author: "Test Author",
  created_at: "2026-01-01",
  updated_at: "2026-01-01",
});
test("autosave serializes edits made while a save is in flight", async () => {
  let release!: (value: EditorialPost) => void;
  const calls: { snapshot: Draft; revision: number }[] = [];
  const first = { ...emptyDraft(), title: "First" },
    next = { ...first, title: "Second" };
  const controller = createSaveController({
    key: "new",
    revision: 0,
    debounceMs: 10000,
    onStatus: () => {},
    onSaved: () => {},
    onConflict: () => {},
    save: async (snapshot, revision) => {
      calls.push({ snapshot, revision });
      if (calls.length === 1)
        return new Promise((resolve) => (release = resolve));
      return post(snapshot, 2);
    },
  });
  controller.changed(first);
  const saving = controller.flush();
  controller.changed(next);
  release(post(first));
  await saving;
  assert.equal(calls.length, 2);
  assert.equal(calls[1].snapshot.title, "Second");
  assert.equal(calls[1].revision, 1);
  assert.equal(controller.state, "saved");
  assert.equal(controller.dirty, false);
  controller.dispose();
});
test("a revision conflict retains a recovery copy and can save on top of the server revision", async () => {
  const mine = { ...emptyDraft(), title: "My changes" },
    latest = post({ ...mine, title: "Server changes" }, 3);
  let blocked = true,
    conflicted = false;
  const controller = createSaveController({
    key: "conflict-test",
    revision: 1,
    debounceMs: 10000,
    onStatus: () => {},
    onSaved: () => {},
    onConflict: () => {
      conflicted = true;
    },
    save: async (snapshot, revision) => {
      if (blocked)
        throw new EditorialError(409, { latest, message: "Conflict" });
      assert.equal(revision, 3);
      return post(snapshot, 4);
    },
  });
  controller.changed(mine);
  await controller.flush();
  assert.equal(controller.state, "conflict");
  assert.ok(conflicted);
  assert.equal(readRecovery("conflict-test")?.snapshot.title, "My changes");
  blocked = false;
  controller.resolve(latest, true);
  await controller.flush();
  assert.equal(controller.revision, 4);
  assert.equal(readRecovery("conflict-test"), null);
  controller.dispose();
});
test("the editor save lifecycle remains active after an effect cleanup and remount", async () => {
  let saves = 0;
  const draft = { ...emptyDraft(), title: "Strict mode draft" };
  const controller = createSaveController({
    key: "strict-test",
    revision: 0,
    debounceMs: 10000,
    onStatus: () => {},
    onSaved: () => {},
    onConflict: () => {},
    save: async (snapshot) => {
      saves++;
      return post(snapshot);
    },
  });
  controller.dispose();
  controller.activate();
  controller.changed(draft);
  await controller.flush();
  assert.equal(saves, 1);
  assert.equal(controller.state, "saved");
  controller.dispose();
});
