const query = new URLSearchParams(location.search);
const returnTo = (() => {
  const value = query.get("return_to");
  return value && value.startsWith("/") && !value.startsWith("//") ? value : "/console.html";
})();
document.querySelector("#return-to").value = returnTo;
document.querySelector("#login-error").hidden = query.get("error") !== "invalid";

// Sign-in options come from the gateway: single sign-on providers (Dex
// connectors such as LDAP, SAML, GitHub, Google) and whether local accounts
// are enabled. Without that answer the local form stays as the fallback.
function ssoLink(provider) {
  const link = document.createElement("a");
  link.className = "button sso-button";
  link.dataset.provider = provider.id;
  const params = new URLSearchParams({connector: provider.id, return_to: returnTo});
  link.href = `/auth/sso/login?${params}`;
  link.textContent = `Sign in with ${provider.name}`;
  if (provider.type) link.dataset.type = provider.type;
  return link;
}

async function loadSignInOptions() {
  let options;
  try {
    const response = await fetch("/auth/providers", {headers: {Accept: "application/json"}});
    if (!response.ok) return;
    options = await response.json();
  } catch {
    return;
  }
  const providers = Array.isArray(options.providers) ? options.providers : [];
  const list = document.querySelector("#sso-options");
  list.replaceChildren(...providers.map(ssoLink));
  list.hidden = providers.length === 0;
  const local = options.local !== false;
  document.querySelector("#local-login").hidden = !local;
  document.querySelector("#login-divider").hidden = !(local && providers.length);
  document.querySelector("#login-hint").textContent = providers.length
    ? (local ? "Use your organization account, or a local account." : "Use your organization account.")
    : "Use your local platform credentials.";
  document.querySelector("#login-note").hidden = providers.length > 0;
}

loadSignInOptions();
