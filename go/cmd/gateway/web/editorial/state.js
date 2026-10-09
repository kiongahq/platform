/* Editorial workspace: deterministic client logic with no DOM dependencies.
 *
 * - SaveController: debounced autosave with a visible status, local
 *   recovery copies, offline handling and 409 conflict reporting.
 * - Recovery: what to offer when a post is reopened after an interruption.
 * - History: block-level undo/redo snapshots (Editor.js has no history).
 *
 * Tested in tests/ui/editorial-state.test.cjs. Exposed as window.EditorialState.
 */
(function (global) {
  "use strict";

  const RECOVERY_PREFIX = "kionga.editorial.recovery.";

  function pad(value) { return String(value).padStart(2, "0"); }
  function clock(date) { return `${pad(date.getHours())}:${pad(date.getMinutes())}`; }

  /* Stable fingerprint of the editable content (key order independent of
   * Editor.js' internal "time" field). */
  function fingerprint(snapshot) {
    if (!snapshot) return "";
    const {title = "", summary = "", slug = "", tags = [], blocks = [], cover = null, seo = {}} = snapshot;
    return JSON.stringify([title, summary, slug, tags, blocks.map(block => [block.id, block.type, block.data]), cover && cover.media_id, cover && cover.alt, seo.title || "", seo.description || ""]);
  }

  function statusLabel(state, savedAt) {
    switch (state) {
      case "saving": return "Saving…";
      case "saved": return savedAt ? `Saved ${clock(savedAt)}` : "Saved";
      case "dirty": return "Unsaved changes";
      case "offline": return "Offline — kept locally";
      case "conflict": return "Newer version on server";
      case "error": return "Save failed — kept locally";
      default: return "";
    }
  }

  function safeStorage(storage) {
    return {
      get(key) { try { const raw = storage && storage.getItem(key); return raw ? JSON.parse(raw) : null; } catch (_) { return null; } },
      set(key, value) { try { storage && storage.setItem(key, JSON.stringify(value)); return true; } catch (_) { return false; } },
      remove(key) { try { storage && storage.removeItem(key); } catch (_) { /* ignore */ } },
    };
  }

  /* Recovery decides whether a stored local copy should be offered for a
   * post as loaded from the server. */
  function recoveryFor(storage, postKey, serverPost) {
    const store = safeStorage(storage);
    const stored = store.get(RECOVERY_PREFIX + postKey);
    if (!stored || !stored.snapshot) return null;
    if (serverPost && fingerprint(stored.snapshot) === fingerprint(serverPost)) {
      store.remove(RECOVERY_PREFIX + postKey);
      return null;
    }
    const serverRevision = serverPost ? serverPost.revision || 0 : 0;
    return {snapshot: stored.snapshot, savedLocallyAt: stored.saved_at, baseRevision: stored.base_revision || 0, stale: (stored.base_revision || 0) < serverRevision};
  }

  function discardRecovery(storage, postKey) { safeStorage(storage).remove(RECOVERY_PREFIX + postKey); }

  /* SaveController.
   *   save(snapshot, baseRevision) -> Promise<post>; rejects with
   *     {status: 409, latest} on conflict, {offline: true} or a TypeError on
   *     network failure, anything else for other failures.
   *   onStatus(state, label), onSaved(post), onConflict(latest, snapshot)
   */
  function createSaveController(options) {
    const {save, storage, key, debounceMs = 1200, now = () => new Date(), onStatus = () => {}, onSaved = () => {}, onConflict = () => {}, isOnline = () => true, setTimer = setTimeout, clearTimer = clearTimeout} = options;
    const store = safeStorage(storage);
    let postKey = key;
    let revision = options.revision || 0;
    let lastSaved = options.initial ? fingerprint(options.initial) : "";
    let pending = null;
    let timer = null;
    let inFlight = null;
    let state = "saved";
    let savedAt = null;
    let blocked = false;

    function setState(next) {
      state = next;
      onStatus(state, statusLabel(state, savedAt));
    }

    function remember(snapshot) {
      store.set(RECOVERY_PREFIX + postKey, {snapshot, base_revision: revision, saved_at: now().toISOString()});
    }

    function changed(snapshot) {
      if (fingerprint(snapshot) === lastSaved && !inFlight) {
        pending = null;
        if (state === "dirty") setState("saved");
        return;
      }
      pending = snapshot;
      remember(snapshot);
      if (blocked) return;
      setState(isOnline() ? "dirty" : "offline");
      if (timer) clearTimer(timer);
      timer = setTimer(() => { timer = null; flush(); }, debounceMs);
    }

    async function flush() {
      if (timer) { clearTimer(timer); timer = null; }
      if (blocked) return null;
      if (inFlight) { await inFlight.catch(() => {}); }
      if (!pending) return null;
      if (!isOnline()) { setState("offline"); return null; }
      const snapshot = pending;
      pending = null;
      setState("saving");
      inFlight = save(snapshot, revision);
      try {
        const post = await inFlight;
        revision = post.revision;
        lastSaved = fingerprint(snapshot);
        savedAt = now();
        if (!pending) store.remove(RECOVERY_PREFIX + postKey);
        setState(pending ? "dirty" : "saved");
        onSaved(post);
        if (pending) return flush();
        return post;
      } catch (error) {
        if (!pending) pending = snapshot;
        remember(pending);
        if (error && error.status === 409) {
          blocked = true;
          setState("conflict");
          onConflict(error.latest, pending);
        } else if (error && (error.offline || error.name === "TypeError")) {
          setState("offline");
        } else {
          setState("error");
        }
        return null;
      } finally {
        inFlight = null;
      }
    }

    return {
      changed,
      flush,
      /* After the user resolves a conflict: adopt the server revision and
       * either keep their snapshot (it will be saved on top) or drop it. */
      resolve(latest, keepMine) {
        blocked = false;
        revision = latest.revision;
        lastSaved = fingerprint(latest);
        if (keepMine && pending) { changed(pending); } else { pending = null; store.remove(RECOVERY_PREFIX + postKey); savedAt = now(); setState("saved"); }
      },
      rekey(newKey, newRevision) {
        const stored = store.get(RECOVERY_PREFIX + postKey);
        if (stored) { store.remove(RECOVERY_PREFIX + postKey); store.set(RECOVERY_PREFIX + newKey, stored); }
        postKey = newKey;
        if (typeof newRevision === "number") revision = newRevision;
      },
      online() { if (pending && !blocked) { setState("dirty"); return flush(); } return Promise.resolve(null); },
      get state() { return state; },
      get revision() { return revision; },
      set revision(value) { revision = value; },
      get dirty() { return Boolean(pending) || Boolean(inFlight); },
    };
  }

  /* History keeps block snapshots for toolbar undo/redo. */
  function createHistory(limit = 100) {
    const past = [];
    const future = [];
    let current = null;
    return {
      record(snapshot) {
        const key = JSON.stringify(snapshot);
        if (current && JSON.stringify(current) === key) return false;
        if (current) past.push(current);
        if (past.length > limit) past.shift();
        current = snapshot;
        future.length = 0;
        return true;
      },
      undo() { if (!past.length) return null; future.push(current); current = past.pop(); return current; },
      redo() { if (!future.length) return null; past.push(current); current = future.pop(); return current; },
      get canUndo() { return past.length > 0; },
      get canRedo() { return future.length > 0; },
    };
  }

  /* Slug follows the title until the author edits it by hand. */
  function slugify(value) {
    return String(value || "").toLowerCase().normalize("NFKD").replace(/[̀-ͯ]/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 80).replace(/-+$/, "");
  }

  /* Blocks that block publication on the client (the server re-checks). */
  function missingAlt(blocks) {
    return (blocks || []).filter(block => block.type === "image" && !String((block.data || {}).alt || "").trim()).map(block => block.id);
  }

  function wordCount(blocks) {
    const text = (blocks || []).map(block => {
      const data = block.data || {};
      const strip = value => String(value || "").replace(/<[^>]*>/g, " ");
      if (block.type === "list") { const walk = items => (items || []).map(item => typeof item === "string" ? strip(item) : `${strip(item.content)} ${walk(item.items)}`).join(" "); return walk(data.items); }
      if (block.type === "table") return (data.content || []).flat().map(strip).join(" ");
      if (block.type === "code") return data.code || "";
      return [data.text, data.caption, data.title, data.message].map(strip).join(" ");
    }).join(" ");
    return text.split(/\s+/).filter(Boolean).length;
  }

  global.EditorialState = {fingerprint, statusLabel, createSaveController, recoveryFor, discardRecovery, createHistory, slugify, missingAlt, wordCount, RECOVERY_PREFIX};
})(typeof window !== "undefined" ? window : globalThis);
