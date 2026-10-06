/* Personal Hub credentials never enter browser storage or generated notebooks. */
function refreshHubProjects() {
  const select = document.querySelector('#hf-project');
  const previous = select.value;
  select.innerHTML = '<option value="">Choose a project</option>' + projectOptions.map(p => `<option value="${escapeHTML(p.id)}">${escapeHTML(p.name)}</option>`).join('');
  select.value = selectedProject || previous;
}

async function loadHuggingFaceAccount() {
  const status = document.querySelector('#hf-account-status');
  try {
    const account = await api('/api/v1/settings/huggingface');
    status.textContent = account.connected ? `Connected as ${account.username}` : account.configured ? 'Not connected. Public model discovery is available without an account.' : 'Ask your administrator to configure KIONGA_CREDENTIAL_KEY to enable encrypted account connections. Public discovery still works.';
    document.querySelector('#hf-account-form button[type=submit]').disabled = !account.configured;
    document.querySelector('#hf-disconnect').hidden = !account.connected;
  } catch (error) { status.textContent = error.message; }
}
document.querySelector('#hf-settings').addEventListener('click', () => showView('settings'));
document.querySelector('#hf-account-form').addEventListener('submit', async event => {
  event.preventDefault();
  const input = document.querySelector('#hf-token'), button = event.submitter || event.target.querySelector('button');
  const token = input.value.trim(); input.value = ''; button.disabled = true;
  document.querySelector('#hf-account-status').textContent = 'Verifying your account…';
  try {
    await api('/api/v1/settings/huggingface', {method:'PUT',body:JSON.stringify({token})});
    await loadHuggingFaceAccount();
  } catch (error) { document.querySelector('#hf-account-status').textContent = error.message; }
  finally { button.disabled = false; }
});
document.querySelector('#hf-disconnect').addEventListener('click', async event => {
  event.target.disabled = true;
  try { await api('/api/v1/settings/huggingface',{method:'DELETE'}); await loadHuggingFaceAccount(); }
  catch(error) { document.querySelector('#hf-account-status').textContent=error.message; }
  finally { event.target.disabled=false; }
});
document.querySelector('#hf-search-form').addEventListener('submit', async event => {
  event.preventDefault(); refreshHubProjects();
  const button=event.target.querySelector('button'), status=document.querySelector('#hf-search-status');
  button.disabled=true;status.textContent='Searching Hugging Face…';
  try {
    const params=new URLSearchParams({search:document.querySelector('#hf-search').value,task:document.querySelector('#hf-task').value});
    const result=await api(`/api/v1/models/huggingface?${params}`);
    document.querySelector('#hf-results').innerHTML=result.items.map(model=>{
      const license=(model.tags || []).find(tag=>tag.startsWith('license:')) || 'license: review model card';
      return `<article class="card"><span class="kind">${escapeHTML(model.pipeline_tag || 'Model')}</span><h3>${escapeHTML(model.id)}</h3><p>${escapeHTML(model.library_name || 'See model card')} · ${Number(model.downloads || 0).toLocaleString()} downloads</p><p>${escapeHTML(license)}${model.gated ? ' · Gated: approval may be required' : ''}${model.private ? ' · Private' : ''}</p><footer><a href="https://huggingface.co/${encodeURI(model.id)}" target="_blank" rel="noopener noreferrer">Model card ↗</a>${can('models_write') ? `<button data-hf-import="${escapeHTML(model.id)}">Register in project</button>` : '<span>Read-only access</span>'}</footer></article>`;
    }).join('');
    status.textContent=result.items.length ? `${result.items.length} results, sorted by downloads. Review the model card and license before registering.` : 'No matching models. Try a repository name or different task.';
  } catch(error) { status.textContent=error.message; }
  finally { button.disabled=false; }
});

let hubNotebookCode='';
function showHubDownload(model) {
  hubNotebookCode=`# Kionga Jupyter includes mlaiops-sdk[huggingface]. See the Hub guide for local installation.\n# Private/gated model? First run: from mlaiops_sdk.huggingface import connect; connect()\nfrom mlaiops_sdk.models import Model\nfrom mlaiops_sdk.huggingface import download_model\n\nmodel = Model.model_validate_json(${JSON.stringify(JSON.stringify(model))})\n# Downloads the pinned commit; does not execute repository code.\npath = download_model(model)\nprint(path)\n`;
  document.querySelector('#hf-code').textContent=hubNotebookCode;
  document.querySelector('#hf-next').hidden=false;
  const workspace=document.querySelector('#hf-workspace');workspace.hidden=!hasService('workbench');workspace.href=`/workspace.html?tool=workbench&project=${encodeURIComponent(model.project_id)}`;
}
document.querySelector('#model-grid').addEventListener('click',event=>{
  const button=event.target.closest('[data-hf-download]');if(!button)return;
  const model=cachedModels.find(item=>item.id===button.dataset.hfDownload);
  if(model){showHubDownload(model);document.querySelector('#hf-next').scrollIntoView({block:'nearest'});}
});
document.querySelector('#hf-results').addEventListener('click',async event=>{
  const button=event.target.closest('[data-hf-import]');if(!button)return;
  const status=document.querySelector('#hf-search-status');
  const projectID=document.querySelector('#hf-project').value;
  if(!projectID){status.textContent='Choose a destination project first.';document.querySelector('#hf-project').focus();return;}
  button.disabled=true;status.textContent='Resolving revision and registering model…';
  try {
    const model=await api('/api/v1/models/huggingface/import',{method:'POST',body:JSON.stringify({project_id:projectID,repo_id:button.dataset.hfImport,revision:document.querySelector('#hf-revision').value || 'main'})});
    showHubDownload(model);
    status.textContent='Model registered at an immutable commit. Download the weights using the code below.';
    await loadModels();
  } catch(error){status.textContent=error.message;}
  finally{button.disabled=false;}
});
document.querySelector('#hf-copy').addEventListener('click',async()=>{
  try{await navigator.clipboard.writeText(hubNotebookCode);toast('Download code copied.');}
  catch{document.querySelector('#hf-search-status').textContent='Clipboard is unavailable. Select and copy the code below.';}
});
document.querySelector('#hf-notebook').addEventListener('click',()=>{
  const notebook={nbformat:4,nbformat_minor:5,metadata:{kernelspec:{display_name:'Python 3',language:'python',name:'python3'}},cells:[{cell_type:'code',id:'download-model',metadata:{},execution_count:null,outputs:[],source:hubNotebookCode.split(/(?<=\n)/)}]};
  const url=URL.createObjectURL(new Blob([JSON.stringify(notebook,null,2)],{type:'application/x-ipynb+json'}));
  const link=document.createElement('a');link.href=url;link.download='huggingface-model.ipynb';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
});
if(document.querySelector('#settings').classList.contains('active'))void loadHuggingFaceAccount();
refreshHubProjects();
