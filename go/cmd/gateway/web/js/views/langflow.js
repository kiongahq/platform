/* Langflow is a second agent canvas, managed independently of native designs. */
let langflowItems = [];
function langflowMessage(message, error = false) {
  const node = document.querySelector('#langflow-message');
  node.textContent = message;
  node.classList.toggle('error', error);
}
async function loadLangflow() {
  const data = await api('/api/v1/langflow-workspaces');
  langflowItems = (data.items || []).filter(item => !selectedProject || item.project_id === selectedProject || item.id === 'local');
  const form = document.querySelector('#langflow-create');
  form.hidden = data.mode !== 'kubernetes' || me.roles.includes('viewer');
  const project = document.querySelector('#langflow-project');
  project.innerHTML = projectOptions.map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)}</option>`).join('');
  if (selectedProject) project.value = selectedProject;
  const list = document.querySelector('#langflow-list');
  list.innerHTML = langflowItems.map(item => `<article class="panel langflow-card">
    <div class="panel-heading"><div><p class="eyebrow">LANGFLOW CANVAS</p><h3>${escapeHTML(item.name)}</h3></div>${status(item.state)}</div>
    <p class="field-help">${escapeHTML(projectOptions.find(project => project.id === item.project_id)?.name || (item.id === 'local' ? 'Local administrator' : item.project_id))}</p>
    ${item.id === 'local' ? '' : `<p class="field-help">${item.cpu} vCPU · ${item.memory_gb} GB memory · ${item.storage_gb} GB storage</p>`}
    <div class="button-row start"><button type="button" class="primary" data-langflow-action="open" data-langflow-id="${escapeHTML(item.id)}" ${!['ready','configured'].includes(item.state) ? 'disabled' : ''}>Open canvas</button>
    ${item.id === 'local' ? '' : `<button type="button" data-langflow-action="${item.state === 'stopped' ? 'start' : 'stop'}" data-langflow-id="${escapeHTML(item.id)}">${item.state === 'stopped' ? 'Start' : 'Stop'}</button><button type="button" data-langflow-action="restart" data-langflow-id="${escapeHTML(item.id)}" ${item.state === 'stopped' ? 'disabled' : ''}>Restart</button><button type="button" class="danger" data-langflow-action="delete" data-langflow-id="${escapeHTML(item.id)}">Delete</button>`}</div>
  </article>`).join('') || emptyState('No Langflow workspaces', data.mode === 'local' ? 'Start the optional Langflow Compose profile to enable the local canvas.' : 'Create a workspace for this project to start building flows.');
}
registerView('langflow', loadLangflow, {eyebrow: 'AGENT BUILDER', title: 'Langflow workspaces'});
document.querySelector('#langflow-refresh').addEventListener('click', () => loadLangflow().catch(error => langflowMessage(error.message, true)));
document.querySelector('#langflow-create').addEventListener('submit', async event => {
  event.preventDefault();
  const payload = {name: document.querySelector('#langflow-name').value.trim(), project_id: document.querySelector('#langflow-project').value, cpu: Number(document.querySelector('#langflow-cpu').value), memory_gb: Number(document.querySelector('#langflow-memory').value), storage_gb: Number(document.querySelector('#langflow-storage').value)};
  try {
    await withBusy(event.submitter, () => api('/api/v1/langflow-workspaces', {method: 'POST', body: JSON.stringify(payload)}));
    event.target.reset();
    langflowMessage('Workspace created. It may take a few minutes to start.');
    await loadLangflow();
  } catch (error) { langflowMessage(error.message, true); }
});
document.querySelector('#langflow-list').addEventListener('click', async event => {
  const button = event.target.closest('[data-langflow-action]');
  if (!button) return;
  const {langflowAction: action, langflowId: id} = button.dataset;
  if (action === 'delete' && !confirm('Delete this Langflow workspace and its stored flows? This cannot be undone.')) return;
  if (action === 'open') {
    const item = langflowItems.find(value => value.id === id);
    location.assign(`/workspace.html?tool=langflow&instance=${encodeURIComponent(id)}${item?.project_id ? `&project=${encodeURIComponent(item.project_id)}` : ''}`);
    return;
  }
  try {
    await withBusy(button, () => api(`/api/v1/langflow-workspaces/${encodeURIComponent(id)}${action === 'delete' ? '' : `/${action}`}`, {method: action === 'delete' ? 'DELETE' : 'POST'}));
    langflowMessage(`Workspace ${action === 'delete' ? 'deleted' : action + ' requested'}.`);
    await loadLangflow();
  } catch (error) { langflowMessage(error.message, true); }
});
