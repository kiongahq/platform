/* Apply the shared appearance preference before styles paint. Workspace and
 * editorial pages use the same choice as the console, including in new tabs. */
(() => {
  function syncAppearance() {
    try {
      const preferences = JSON.parse(localStorage.getItem("kionga.console.preferences") || "{}");
      if (preferences.theme === "light" || preferences.theme === "dark") document.documentElement.dataset.theme = preferences.theme;
      else delete document.documentElement.dataset.theme;
    } catch { delete document.documentElement.dataset.theme; }
  }
  syncAppearance();
  window.addEventListener("storage", event => {
    if (event.key === "kionga.console.preferences" || event.key === null) syncAppearance();
  });
})();
