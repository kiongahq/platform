/* Langflow is a second agent canvas, managed independently of native designs. */
let langflowItems = [];
let langflowSnapshots = [];
let langflowAgents = [];
let langflowCaptureWorkspace = '';
let langflowManagedMode = false;
let langflowPublishSnapshot = '';
function langflowMessage(message, error = false) {
  const node = document.querySelector('#langflow-message');
  node.textContent = message;
  node.classList.toggle('error', error);
}
async function loadLangflow() {
  const [data, snapshots, agents, datasets, knowledge, connections] = await Promise.all([api('/api/v1/langflow-workspaces'), api('/api/v1/langflow-snapshots'), me.services.includes('agents') || me.roles.some(role => ['admin','operator'].includes(role)) ? api('/api/v1/agents') : Promise.resolve({items:[]}), api('/api/v1/datasets'), api('/api/v1/knowledge-bases'), api('/api/v1/agent-api-connections')]);
  langflowSnapshots = (snapshots.items || []).filter(item => !selectedProject || item.project_id === selectedProject);
  langflowAgents = agents.items || [];
  langflowManagedMode = data.mode === 'kubernetes';
  langflowItems = (data.items || []).filter(item => !selectedProject || item.project_id === selectedProject || item.id === 'local');
  const projectDatasets = (datasets.items || []).filter(item => !selectedProject || item.project_id === selectedProject);
  document.querySelector('#langflow-datasets').innerHTML = projectDatasets.map(item => `<div class="store-row"><div><b>${escapeHTML(item.name)}</b><small>${escapeHTML(item.description || 'Project dataset')} · <code>${escapeHTML(item.id)}</code></small></div><div><span class="tag">${escapeHTML(item.bucket)}</span><small>${escapeHTML(item.prefix || 'Entire bucket')}</small></div></div>`).join('') || emptyState('No datasets', 'An administrator can assign a bucket prefix to this project.');
  const datasetForm = document.querySelector('#langflow-dataset-create');
  datasetForm.hidden = !me.roles.some(role => ['admin','operator'].includes(role));
  datasetForm.elements.project_id.innerHTML = projectOptions.map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)}</option>`).join('');
  if (selectedProject) datasetForm.elements.project_id.value = selectedProject;
  const bases = (knowledge.items || []).filter(item => !selectedProject || item.project_id === selectedProject);
  document.querySelector('#langflow-knowledge').innerHTML = bases.map(item => `<div class="store-row"><div><b>${escapeHTML(item.name)}</b><small>${escapeHTML(projectOptions.find(project => project.id === item.project_id)?.name || item.project_id)} · <code>${escapeHTML(item.id)}</code></small></div><div><span class="tag">${escapeHTML(item.backend)}</span><small>${Number(item.dimensions)} dimensions</small></div></div>`).join('') || emptyState('No knowledge bases', 'An administrator can register Qdrant or pgvector for this project.');
  const knowledgeForm = document.querySelector('#langflow-knowledge-create');
  knowledgeForm.hidden = !me.roles.some(role => ['admin','operator'].includes(role));
  knowledgeForm.elements.project_id.innerHTML = projectOptions.map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)}</option>`).join('');
  if (selectedProject) knowledgeForm.elements.project_id.value = selectedProject;
  const apis = (connections.items || []).filter(item => !selectedProject || item.project_id === selectedProject);
  document.querySelector('#langflow-api-connections').innerHTML = apis.map(item => `<div class="store-row"><div><b>${escapeHTML(item.name)}</b><small>${escapeHTML(item.description || 'Approved API operation')}</small></div><div><code>${escapeHTML(item.id)}</code><small>${escapeHTML(projectOptions.find(project => project.id === item.project_id)?.name || item.project_id)}</small></div></div>`).join('') || emptyState('No approved API connections', 'An administrator can register a project API operation.');
  const apiForm = document.querySelector('#langflow-api-create');
  apiForm.hidden = !me.roles.some(role => ['admin','operator'].includes(role));
  apiForm.elements.project_id.innerHTML = projectOptions.map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)}</option>`).join('');
  if (selectedProject) apiForm.elements.project_id.value = selectedProject;
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
    <div class="button-row start"><button type="button" class="primary" data-langflow-action="open" data-langflow-id="${escapeHTML(item.id)}" ${!['ready','configured'].includes(item.state) ? 'disabled' : ''}>Open canvas</button><button type="button" data-langflow-action="flows" data-langflow-id="${escapeHTML(item.id)}" ${!['ready','configured'].includes(item.state) ? 'disabled' : ''}>Capture flow</button>
    ${item.id === 'local' ? '' : `<button type="button" data-langflow-action="${item.state === 'stopped' ? 'start' : 'stop'}" data-langflow-id="${escapeHTML(item.id)}">${item.state === 'stopped' ? 'Start' : 'Stop'}</button><button type="button" data-langflow-action="restart" data-langflow-id="${escapeHTML(item.id)}" ${item.state === 'stopped' ? 'disabled' : ''}>Restart</button><button type="button" class="danger" data-langflow-action="delete" data-langflow-id="${escapeHTML(item.id)}">Delete</button>`}</div>
  </article>`).join('') || emptyState('No Langflow workspaces', data.mode === 'local' ? 'Start the optional Langflow Compose profile to enable the local canvas.' : 'Create a workspace for this project to start building flows.');
  document.querySelector('#langflow-snapshots').innerHTML = langflowSnapshots.map(item => {
    const canManage = langflowManagedMode && item.owner_subject === me.subject && (me.services.includes('agents') || me.roles.some(role => ['admin','operator'].includes(role)));
    const deployments = langflowAgents.filter(agent => agent.snapshot_id === item.id).map(agent => `<small>Agent ${escapeHTML(agent.id)} · revision ${agent.snapshot_revision} · ${escapeHTML(agent.status)}${item.active_agent_id === agent.id ? ' · active' : ''}</small>${canManage && agent.status === 'ready' && item.active_agent_id !== agent.id ? `<button type="button" data-langflow-activate="${escapeHTML(agent.id)}" data-langflow-snapshot="${escapeHTML(item.id)}">${item.active_agent_id && agent.snapshot_revision < item.active_revision ? 'Roll back to' : 'Activate'} revision ${agent.snapshot_revision}</button>` : ''}`).join('');
    return `<div class="store-row"><div><b>${escapeHTML(item.name)}</b><small>${escapeHTML(projectOptions.find(project => project.id === item.project_id)?.name || item.project_id)} · ${escapeHTML(item.flow_id)}</small>${item.active_agent_id ? `<small>Stable API · <code>${escapeHTML(item.invoke_path)}</code></small>` : ''}${deployments}</div><div>${status(`revision ${item.revision}`)}<small>${escapeHTML(dateTime(item.updated_at))}</small>${canManage ? `<button type="button" data-langflow-publish="${escapeHTML(item.id)}">Publish</button>` : ''}</div></div>`;
  }).join('') || emptyState('No flow snapshots', 'Open a Langflow workspace and capture a saved flow.');
}
registerView('langflow', loadLangflow, {eyebrow: 'AGENT BUILDER', title: 'Langflow workspaces'});
document.querySelector('#langflow-dataset-create').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.target;
  const error = document.querySelector('#langflow-dataset-error');
  error.textContent = '';
  try {
    await withBusy(event.submitter, () => api('/api/v1/admin/datasets', {method:'POST', body:JSON.stringify(Object.fromEntries(new FormData(form)))}));
    form.reset();
    toast('Dataset registered for this project.');
    await loadLangflow();
  } catch (failure) { error.textContent = failure.message; }
});
document.querySelector('#langflow-knowledge-create [name=backend]').addEventListener('change', event => {
  const postgres = event.target.value === 'pgvector';
  document.querySelectorAll('[data-knowledge-qdrant]').forEach(node => { node.hidden = postgres; });
  document.querySelectorAll('[data-knowledge-pgvector]').forEach(node => { node.hidden = !postgres; });
});
document.querySelector('#langflow-knowledge-create').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.target;
  const error = document.querySelector('#langflow-knowledge-error');
  error.textContent = '';
  const payload = Object.fromEntries(new FormData(form));
  payload.dimensions = Number(payload.dimensions);
  if (payload.backend === 'qdrant') delete payload.dsn_ref;
  else { delete payload.endpoint; delete payload.secret_ref; }
  try {
    await withBusy(event.submitter, () => api('/api/v1/admin/knowledge-bases', {method:'POST', body:JSON.stringify(payload)}));
    form.reset();
    form.elements.backend.dispatchEvent(new Event('change'));
    toast('Knowledge base registered for this project.');
    await loadLangflow();
  } catch (failure) { error.textContent = failure.message; }
});
document.querySelector('#langflow-api-create').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.target;
  const error = document.querySelector('#langflow-api-error');
  error.textContent = '';
  try {
    const payload = Object.fromEntries(new FormData(form));
    await withBusy(event.submitter, () => api('/api/v1/admin/agent-api-connections', {method:'POST', body:JSON.stringify(payload)}));
    form.reset();
    toast('Agent API operation registered for this project.');
    await loadLangflow();
  } catch (failure) { error.textContent = failure.message; }
});
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
  if (action === 'flows') {
    try {
      const data = await withBusy(button, () => api(`/api/v1/langflow-workspaces/${encodeURIComponent(id)}/flows`));
      const flows = data.items || [];
      if (!flows.length) throw new Error('Save a flow in the Langflow canvas first.');
      langflowCaptureWorkspace = id;
      document.querySelector('#langflow-flow-choice').innerHTML = flows.map(flow => `<option value="${escapeHTML(flow.id)}">${escapeHTML(flow.name || flow.id)}</option>`).join('');
      document.querySelector('#langflow-capture-project').innerHTML = projectOptions.map(project => `<option value="${escapeHTML(project.id)}">${escapeHTML(project.name)}</option>`).join('');
      const workspace = langflowItems.find(item => item.id === id);
      document.querySelector('#langflow-capture-project').value = workspace?.project_id || selectedProject || projectOptions[0]?.id || '';
      document.querySelector('#langflow-capture-project').disabled = Boolean(workspace?.project_id);
      document.querySelector('#langflow-capture-error').textContent = '';
      langflowSnapshotChoices();
      document.querySelector('#langflow-capture-dialog').showModal();
    } catch (error) { langflowMessage(error.message, true); }
    return;
  }
  try {
    await withBusy(button, () => api(`/api/v1/langflow-workspaces/${encodeURIComponent(id)}${action === 'delete' ? '' : `/${action}`}`, {method: action === 'delete' ? 'DELETE' : 'POST'}));
    langflowMessage(`Workspace ${action === 'delete' ? 'deleted' : action + ' requested'}.`);
    await loadLangflow();
  } catch (error) { langflowMessage(error.message, true); }
});
function langflowSnapshotChoices() {
  const flow = document.querySelector('#langflow-flow-choice').value;
  const project = document.querySelector('#langflow-capture-project').value;
  const options = langflowSnapshots.filter(item => item.flow_id === flow && item.workspace_id === langflowCaptureWorkspace && item.project_id === project);
  document.querySelector('#langflow-snapshot-choice').innerHTML = `<option value="">New snapshot</option>${options.map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)} · revision ${item.revision}</option>`).join('')}`;
}
document.querySelector('#langflow-flow-choice').addEventListener('change', langflowSnapshotChoices);
document.querySelector('#langflow-capture-project').addEventListener('change', langflowSnapshotChoices);
document.querySelector('#langflow-capture-cancel').addEventListener('click', () => document.querySelector('#langflow-capture-dialog').close());
document.querySelector('#langflow-capture-form').addEventListener('submit', async event => {
  event.preventDefault();
  const flow = document.querySelector('#langflow-flow-choice').value;
  const project = document.querySelector('#langflow-capture-project').value;
  const id = document.querySelector('#langflow-snapshot-choice').value;
  const current = langflowSnapshots.find(item => item.id === id);
  const payload = {flow_id: flow, project_id: project, snapshot_id: id, revision: current?.revision || 0};
  try {
    await withBusy(event.submitter, () => api(`/api/v1/langflow-workspaces/${encodeURIComponent(langflowCaptureWorkspace)}/snapshots`, {method:'POST', body:JSON.stringify(payload)}));
    document.querySelector('#langflow-capture-dialog').close();
    langflowMessage('Flow snapshot saved as a new Kionga revision.');
    await loadLangflow();
  } catch (error) { document.querySelector('#langflow-capture-error').textContent = error.message; }
});
document.querySelector('#langflow-snapshots').addEventListener('click', async event => {
  const activate = event.target.closest('[data-langflow-activate]');
  if (activate) {
    try {
      await withBusy(activate, () => api(`/api/v1/langflow-snapshots/${encodeURIComponent(activate.dataset.langflowSnapshot)}/activate`, {method:'POST',body:JSON.stringify({agent_id:activate.dataset.langflowActivate})}));
      toast('Active Langflow deployment updated. The stable API now uses this revision.');
      await loadLangflow();
    } catch (error) { langflowMessage(error.message, true); }
    return;
  }
  const button = event.target.closest('[data-langflow-publish]');
  if (!button) return;
  const snapshot = langflowSnapshots.find(item => item.id === button.dataset.langflowPublish);
  if (!snapshot) return;
  langflowPublishSnapshot = snapshot.id;
  const revisions = [snapshot.revision, ...(snapshot.history || []).map(item => item.revision)].sort((a,b) => b-a);
  document.querySelector('#langflow-publish-revision').innerHTML = revisions.map(revision => `<option value="${revision}">Revision ${revision}${revision === snapshot.revision ? ' · latest' : ''}</option>`).join('');
  document.querySelector('#langflow-publish-error').textContent = '';
  document.querySelector('#langflow-publish-dialog').showModal();
});
document.querySelector('#langflow-publish-cancel').addEventListener('click', () => document.querySelector('#langflow-publish-dialog').close());
document.querySelector('#langflow-publish-form').addEventListener('submit', async event => {
  event.preventDefault();
  const payload = {revision: Number(document.querySelector('#langflow-publish-revision').value), autoscaling: {min_replicas: Number(document.querySelector('#langflow-publish-min').value), max_replicas: Number(document.querySelector('#langflow-publish-max').value)}, resources: {cpu: document.querySelector('#langflow-publish-cpu').value, memory: document.querySelector('#langflow-publish-memory').value}};
  try {
    await withBusy(event.submitter, () => api(`/api/v1/langflow-snapshots/${encodeURIComponent(langflowPublishSnapshot)}/publish`, {method:'POST', body:JSON.stringify(payload)}));
    document.querySelector('#langflow-publish-dialog').close();
    langflowMessage(`Revision ${payload.revision} queued as a managed agent. Open Agents to check deployment status.`);
  } catch (error) { document.querySelector('#langflow-publish-error').textContent = error.message; }
});
