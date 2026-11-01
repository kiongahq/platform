import {
  fingerprint,
  saveRecovery,
  clearRecovery,
  readRecovery,
  EditorialError,
  type Draft,
  type EditorialPost,
} from "./editorial";
export type SaveState =
  | "saved"
  | "dirty"
  | "saving"
  | "offline"
  | "conflict"
  | "error";
type Options = {
  key: string;
  revision: number;
  initial?: Draft;
  save: (snapshot: Draft, revision: number) => Promise<EditorialPost>;
  onStatus: (state: SaveState, label: string) => void;
  onSaved: (post: EditorialPost) => void;
  onConflict: (latest: EditorialPost, snapshot: Draft) => void;
  debounceMs?: number;
};
export function createSaveController(options: Options) {
  let key = options.key,
    revision = options.revision,
    lastSaved = options.initial ? fingerprint(options.initial) : "",
    pending: Draft | null = null,
    timer: ReturnType<typeof setTimeout> | null = null,
    inFlight: Promise<EditorialPost> | null = null,
    state: SaveState = "saved",
    blocked = false,
    disposed = false;
  const online = () =>
    typeof navigator === "undefined" || navigator.onLine !== false;
  const setState = (next: SaveState) => {
    state = next;
    const labels: Record<SaveState, string> = {
      saved: `Saved ${new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })}`,
      dirty: "Unsaved changes",
      saving: "Saving…",
      offline: "Offline — kept locally",
      conflict: "Newer version on server",
      error: "Save failed — kept locally",
    };
    if (!disposed) options.onStatus(next, labels[next]);
  };
  const remember = (snapshot: Draft) =>
    saveRecovery(key, {
      snapshot,
      base_revision: revision,
      saved_at: new Date().toISOString(),
    });
  function changed(snapshot: Draft) {
    if (fingerprint(snapshot) === lastSaved && !inFlight) {
      pending = null;
      if (state === "dirty") setState("saved");
      return;
    }
    pending = snapshot;
    remember(snapshot);
    if (blocked || disposed) return;
    setState(online() ? "dirty" : "offline");
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => void flush(), options.debounceMs ?? 1200);
  }
  async function flush(): Promise<EditorialPost | null> {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    if (blocked) return null;
    if (inFlight) await inFlight.catch(() => {});
    if (!pending) return null;
    if (!online()) {
      setState("offline");
      return null;
    }
    const snapshot = pending;
    pending = null;
    setState("saving");
    inFlight = options.save(snapshot, revision);
    try {
      const post = await inFlight;
      revision = post.revision;
      lastSaved = fingerprint(snapshot);
      if (!pending) clearRecovery(key);
      setState(pending ? "dirty" : "saved");
      if (!disposed) options.onSaved(post);
      inFlight = null;
      if (pending) return await flush();
      return post;
    } catch (error) {
      if (!pending) pending = snapshot;
      remember(pending);
      if (
        error instanceof EditorialError &&
        error.status === 409 &&
        error.latest
      ) {
        blocked = true;
        setState("conflict");
        if (!disposed) options.onConflict(error.latest, pending);
      } else
        setState(error instanceof TypeError || !online() ? "offline" : "error");
      return null;
    } finally {
      inFlight = null;
    }
  }
  return {
    activate() {
      disposed = false;
    },
    changed,
    flush,
    resolve(latest: EditorialPost, keepMine: boolean) {
      blocked = false;
      revision = latest.revision;
      lastSaved = fingerprint(latest);
      if (keepMine && pending) changed(pending);
      else {
        pending = null;
        clearRecovery(key);
        setState("saved");
      }
    },
    rekey(next: string, nextRevision: number) {
      const stored = readRecovery(key);
      if (stored) {
        clearRecovery(key);
        saveRecovery(next, stored);
      }
      key = next;
      revision = nextRevision;
    },
    online() {
      return pending && !blocked ? flush() : Promise.resolve(null);
    },
    dispose() {
      if (timer) clearTimeout(timer);
      disposed = true;
    },
    get state() {
      return state;
    },
    get revision() {
      return revision;
    },
    get dirty() {
      return !!pending || !!inFlight;
    },
  };
}
