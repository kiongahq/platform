const query = new URLSearchParams(location.search);
const project = query.get("project") || "";
const tool = document.querySelector("#workspace-tool");
tool.value = ["workbench","ide"].includes(query.get("tool")) ? query.get("tool") : "workbench";
document.querySelector("#back-console").href = `/console.html${project ? `?project=${encodeURIComponent(project)}` : ""}`;
const message = document.querySelector("#workspace-message");
const loading = document.querySelector(".workspace-loading");
const frame = document.querySelector("#workspace-frame");
const retry = document.querySelector("#workspace-retry");
async function workspaceAPI(path, options = {}) {
  const response = await fetch(path, {signal:AbortSignal.timeout(20000), ...options, headers:{"Content-Type":"application/json"}});
  if (response.status === 401) { location.assign(`/auth/login?return_to=${encodeURIComponent(location.pathname + location.search)}`); throw new Error("Please sign in again."); }
  if (!response.headers.get("content-type")?.includes("application/json")) throw new Error("Workspace API is unavailable. Try again.");
  const body = await response.json();
  if (!response.ok) throw new Error(body.message || "Workspace could not be opened.");
  return body;
}
async function launch() {
  loading.hidden=false;message.hidden=false; message.textContent="Preparing your workspace…";retry.hidden=true;tool.disabled=true;frame.hidden=true;
  document.querySelector("#workspace-status").textContent="Opening workspace…";
  try {
    const [workspaces, projects] = await Promise.all([workspaceAPI("/api/v1/workspaces"), workspaceAPI("/api/v1/project-options")]);
    for (const option of tool.options) { const state=workspaces.items.find(item=>item.id===option.value);option.disabled=!state?.available; }
    if (tool.selectedOptions[0]?.disabled) {
      const available=Array.from(tool.options).find(option=>!option.disabled);
      if (available) tool.value=available.value;
    }
    const state=workspaces.items.find(item=>item.id===tool.value);
    if (!state?.available) throw new Error(state?.message || "Workspace is unavailable.");
    document.querySelector("#workspace-project").textContent=projects.items.find(item=>item.id===project)?.name || "Shared workspace";
    const result=await workspaceAPI(`/api/v1/workspaces/${tool.value}/launch`,{method:"POST",body:JSON.stringify({project_id:project})});
    if (result.ticket) {
      const origin = new URL(result.url).origin;
      if (origin === location.origin || !origin.startsWith("https://")) throw new Error("Workspace isolation is misconfigured.");
      frame.hidden=false;
      frame.name = "kionga-isolated-workspace";
      const form = document.createElement("form");
      form.method = "POST"; form.action = result.url; form.target = frame.name; form.hidden = true;
      for (const [name, value] of Object.entries({ticket: result.ticket, next: result.next})) {
        const input = document.createElement("input"); input.type = "hidden"; input.name = name; input.value = value; form.append(input);
      }
      document.body.append(form); form.submit(); form.remove();
    } else {
      frame.hidden=false;
      frame.src=result.url;
    }
    loading.hidden=true;
    document.querySelector("#workspace-status").textContent="Shared files · signed in with Kionga";
    query.set("tool",tool.value);history.replaceState({},"",`${location.pathname}?${query}`);
  } catch(error) { frame.hidden=true;message.textContent=error.message;retry.hidden=false;document.querySelector("#workspace-status").textContent="Workspace unavailable"; }
  finally { tool.disabled=false; }
}
tool.addEventListener("change",launch);retry.addEventListener("click",launch);launch();
