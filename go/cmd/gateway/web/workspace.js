const query = new URLSearchParams(location.search);
const project = query.get("project") || "";
const tool = document.querySelector("#workspace-tool");
tool.value = ["workbench","ide"].includes(query.get("tool")) ? query.get("tool") : "workbench";
document.querySelector("#back-console").href = `/console.html${project ? `?project=${encodeURIComponent(project)}` : ""}`;
const message = document.querySelector("#workspace-message");
const frame = document.querySelector("#workspace-frame");
const retry = document.querySelector("#workspace-retry");
async function workspaceAPI(path, options = {}) {
  const response = await fetch(path, {signal:AbortSignal.timeout(20000), ...options, headers:{"Content-Type":"application/json"}});
  if (response.status === 401) { location.assign(`/auth/login?return_to=${encodeURIComponent(location.pathname + location.search)}`); throw new Error("Please sign in again."); }
  const body = await response.json();
  if (!response.ok) throw new Error(body.message || "Workspace could not be opened.");
  return body;
}
async function launch() {
  message.hidden=false; message.textContent="Preparing your workspace…";retry.hidden=true;tool.disabled=true;frame.hidden=true;
  try {
    const [workspaces, projects] = await Promise.all([workspaceAPI("/api/v1/workspaces"), workspaceAPI("/api/v1/project-options")]);
    for (const option of tool.options) { const state=workspaces.items.find(item=>item.id===option.value);option.disabled=!state?.available; }
    const state=workspaces.items.find(item=>item.id===tool.value);
    if (!state?.available) throw new Error(state?.message || "Workspace is unavailable.");
    document.querySelector("#workspace-project").textContent=projects.items.find(item=>item.id===project)?.name || "Shared workspace";
    const result=await workspaceAPI(`/api/v1/workspaces/${tool.value}/launch`,{method:"POST",body:JSON.stringify({project_id:project})});
    frame.src=result.url;frame.hidden=false;message.hidden=true;
    document.querySelector("#workspace-status").textContent="Shared files · signed in with Kionga";
    query.set("tool",tool.value);history.replaceState({},"",`${location.pathname}?${query}`);
  } catch(error) { message.textContent=error.message;retry.hidden=false; }
  finally { tool.disabled=false; }
}
tool.addEventListener("change",launch);retry.addEventListener("click",launch);launch();
