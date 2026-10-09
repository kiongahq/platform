/* Starts the console after every view module has registered itself. */
applyPreferences();
renderIntros();
let initialized = false;
async function initializeConsole() {
  try {
    await loadMe();
    await Promise.allSettled([loadProjectOptions(), loadWorkspaces()]);
    const query = new URLSearchParams(location.search);
    const preferred = query.get("view") || readPreferences().startView;
    const first = preferred && viewLoaders[preferred] && !canOpenView(preferred) ? preferred : (hasService("overview") ? "overview" : "profile");
    history.replaceState({}, "", routeURL(first, query.get("resource") || ""));
    await showView(first, {history: false, resource: query.get("resource") || "", focus: false});
    if (hasService("overview") && readPreferences().live !== false) connectEvents();
    else setLiveState("off", "Live updates off");
    initialized = true;
  } catch (error) { feedback(error.message, true); }
}
initializeConsole();
