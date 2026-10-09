/* Settings: profile, Hugging Face account, API keys, preferences,
 * workspace entry points and session. */
function writePreferences() {
  const prefs = {
    compact: document.querySelector("#setting-compact").checked,
    reduceMotion: document.querySelector("#setting-motion").checked,
    live: document.querySelector("#setting-live").checked,
    startView: document.querySelector("#setting-start-view").value,
    theme: document.querySelector("#setting-theme").value,
  };
  try { localStorage.setItem(preferenceKey, JSON.stringify(prefs)); }
  catch { toast("Preferences could not be saved in this browser.", "error"); }
  applyPreferences();
  if (prefs.live && hasService("overview")) connectEvents();
  if (!prefs.live && eventSource) { eventSource.close(); eventSource = null; setLiveState("off", "Live updates off"); }
  document.querySelector("#preferences-status").textContent = "Saved in this browser.";
}

async function loadSettings() {
  if (typeof loadHuggingFaceAccount === "function") void loadHuggingFaceAccount();
  const data = await api("/api/v1/settings/tokens");
  const prefs = readPreferences();
  document.querySelector("#setting-compact").checked = Boolean(prefs.compact);
  document.querySelector("#setting-motion").checked = Boolean(prefs.reduceMotion);
  document.querySelector("#setting-live").checked = prefs.live !== false;
  document.querySelector("#setting-start-view").value = prefs.startView || "overview";
  document.querySelector("#setting-theme").value = prefs.theme || "system";
  document.querySelector("#settings-profile").innerHTML = `<dl class="settings-definition"><div><dt>Name</dt><dd>${escapeHTML(me.email || me.subject)}</dd></div><div><dt>Subject</dt><dd>${copyable(me.subject)}</dd></div><div><dt>Role</dt><dd>${me.roles.map(role => `<span class="role-badge ${escapeHTML(role)}">${escapeHTML(role)}</span>`).join(" ") || "—"}</dd></div><div><dt>Sign-in</dt><dd>${escapeHTML(me.mode === "oidc" ? "Identity provider (OIDC)" : "Local account")}</dd></div></dl>`;
  document.querySelector("#settings-auth-mode").textContent = me.mode === "oidc" ? "Signed in with your identity provider" : "Signed in with a local account";
  document.querySelector("#settings-session-identity").textContent = me.email || me.subject;
  await loadWorkspaces().catch(() => {});
  document.querySelector("#settings-jupyter").hidden = !hasService("workbench");
  document.querySelector("#settings-ide").hidden = !hasService("ide");
  const items = data.items || [];
  document.querySelector("#api-token-list").innerHTML = items.length ? items.map(token => `<div class="token-row"><div class="token-mark" aria-hidden="true">⌁</div><div><b>${escapeHTML(token.name)}</b><small><code>${escapeHTML(token.prefix)}…</code> · ${token.services.map(escapeHTML).join(", ") || "no services"}</small><small>Expires ${escapeHTML(dateTime(token.expires_at))} · ${token.last_used_at ? `Last used ${escapeHTML(dateTime(token.last_used_at))}` : "Never used"}</small></div>${token.revoked_at ? status("revoked") : `<button type="button" class="danger" data-token-revoke="${escapeHTML(token.id)}">Revoke</button>`}</div>`).join("") : emptyState("No personal API keys", "Create a scoped key for the CLI, SDK, or local scripts.");
}

document.querySelectorAll("#setting-compact,#setting-motion,#setting-live,#setting-start-view,#setting-theme").forEach(control => control.addEventListener("change", writePreferences));
document.querySelector("#create-api-token").addEventListener("click", () => {
  const form = document.querySelector("#api-token-form"); form.reset();
  const scopes = accessServices.filter(service => !["workbench", "ide"].includes(service) && hasService(service));
  document.querySelector("#token-service-scopes").innerHTML = scopes.map(service => `<label class="inline-check"><input type="checkbox" name="services" value="${escapeHTML(service)}"> ${escapeHTML(service)}</label>`).join("");
  document.querySelector("#api-token-error").textContent = "";
  document.querySelector("#api-token-dialog").showModal();
});
document.querySelector("#api-token-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#api-token-error");
  error.textContent = "";
  const payload = {name: form.elements.name.value, expires_in_days: Number(form.elements.expires_in_days.value), project_ids: csv(form.elements.project_ids.value), services: [...form.querySelectorAll("[name='services']:checked")].map(input => input.value)};
  try {
    if (!payload.services.length) throw new Error("Choose at least one service scope.");
    const created = await withBusy(event.submitter, () => api("/api/v1/settings/tokens", {method: "POST", body: JSON.stringify(payload)}), {failure: false});
    document.querySelector("#api-token-dialog").close();
    document.querySelector("#api-token-secret").textContent = created.secret;
    document.querySelector("#api-token-secret-dialog").showModal();
    await loadSettings();
  } catch (failure) { error.textContent = failure.message; }
});
document.querySelector("#copy-api-token").addEventListener("click", async event => {
  try { await copyText(document.querySelector("#api-token-secret").textContent); flashButton(event.currentTarget, "success", "Copied ✓"); }
  catch (failure) { toast(`${failure.message} Select the key and copy it manually.`, "error"); }
});
onClick("[data-token-revoke]", async node => {
  if (!confirm("Revoke this API key? Any tool using it immediately loses access.")) return;
  await withBusy(node, () => api(`/api/v1/settings/tokens/${encodeURIComponent(node.dataset.tokenRevoke)}`, {method: "DELETE"}), {success: "API key revoked."});
  await loadSettings();
});

registerView("settings", loadSettings, {
  eyebrow: "ACCOUNT & PLATFORM", title: "Settings",
});
