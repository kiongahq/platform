const storageKey = "kionga.console.preferences";

// Earlier clients persisted inferred themes. Reset that legacy choice once;
// current clients persist a version only after migration or explicit selection.
export function normalizeConsolePreferences(value: unknown): Record<string, unknown> {
  const stored = value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown> : {};
  return {
    ...stored,
    theme: stored.themeVersion === 1 && stored.theme === "dark" ? "dark" : "light",
    themeVersion: 1,
  };
}

export function readConsolePreferences(): Record<string, unknown> {
  try {
    const raw = localStorage.getItem(storageKey);
    const stored = JSON.parse(raw || "{}");
    const preferences = normalizeConsolePreferences(stored);
    if (raw && stored?.themeVersion !== 1) {
      localStorage.setItem(storageKey, JSON.stringify(preferences));
    }
    return preferences;
  } catch {
    return normalizeConsolePreferences(null);
  }
}
