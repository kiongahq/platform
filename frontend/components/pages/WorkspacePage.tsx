"use client";
import {Select} from "@/components/ui/Select";
import { useCallback, useEffect, useRef, useState } from "react";

type Tool = 'workbench' | 'ide' | 'langflow';
type Selection = { tool: Tool; project: string; instance: string };
type Workspace = { id: string; available: boolean; message?: string };
type Launch = { url: string; ticket?: string; next?: string };
async function workspaceRequest<T>(path: string, signal: AbortSignal, body?: unknown): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin', signal, method: body === undefined ? 'GET' : 'POST', headers: { Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
  if (response.status === 401) { window.location.assign(`/auth/login?return_to=${encodeURIComponent(window.location.pathname + window.location.search)}`); throw new Error('Please sign in again.'); }
  if (!response.headers.get('content-type')?.includes('application/json')) throw new Error('Workspace API is unavailable. Try again.');
  const data = await response.json();
  if (!response.ok) throw new Error(data.message || 'Workspace could not be opened.');
  return data;
}
export function WorkspacePage() {
  const [selection, setSelection] = useState<Selection | null>(null);
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [projectName, setProjectName] = useState('Shared workspace');
  const [launch, setLaunch] = useState<Launch | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  const handoff = useRef<HTMLFormElement>(null);
  const read = useCallback(() => { const query = new URLSearchParams(window.location.search); const value = query.get('tool'); setSelection({ tool: value === 'ide' || value === 'langflow' ? value : 'workbench', project: query.get('project') || '', instance: query.get('instance') || '' }); }, []);
  useEffect(() => { read(); window.addEventListener('popstate', read); return () => window.removeEventListener('popstate', read); }, [read]);
  useEffect(() => {
    if (!selection) return;
    if (selection.tool === 'langflow' && !selection.instance) { window.location.replace(`/console/langflow${selection.project ? `?project=${encodeURIComponent(selection.project)}` : ''}`); return; }
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(new Error('Workspace request timed out. Please retry.')), 20000);
    setLoading(true); setError(''); setLaunch(null);
    (async () => {
      const [tools, projects] = await Promise.all([
        workspaceRequest<{ items: Workspace[] }>('/api/v1/workspaces', controller.signal),
        workspaceRequest<{ items: { id: string; name: string }[] }>('/api/v1/project-options', controller.signal),
      ]);
      if (controller.signal.aborted) return;
      setWorkspaces(tools.items || []);
      setProjectName(projects.items.find(item => item.id === selection.project)?.name || 'Shared workspace');
      const tool = tools.items.find(item => item.id === selection.tool);
      if (selection.tool !== 'langflow' && !tool?.available) throw new Error(tool?.message || 'This workspace is unavailable.');
      const path = selection.tool === 'langflow' ? `/api/v1/langflow-workspaces/${encodeURIComponent(selection.instance)}/launch` : `/api/v1/workspaces/${selection.tool}/launch`;
      const result = await workspaceRequest<Launch>(path, controller.signal, { project_id: selection.project });
      if (result.ticket) {
        const target = new URL(result.url);
        if (target.protocol !== 'https:' || target.origin === window.location.origin) throw new Error('Workspace isolation is misconfigured.');
      } else {
        const target = new URL(result.url, window.location.origin);
        if (target.origin !== window.location.origin || !target.pathname.startsWith('/workspaces/')) throw new Error('The workspace launch address is invalid.');
      }
      if (!controller.signal.aborted) setLaunch(result);
    })().catch(failure => { if (!controller.signal.aborted || controller.signal.reason instanceof Error && controller.signal.reason.name !== 'AbortError') setError(failure.message || 'Workspace could not be opened.'); }).finally(() => { if (!controller.signal.aborted) setLoading(false); window.clearTimeout(timer); });
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [selection, attempt]);
  useEffect(() => { if (launch?.ticket) handoff.current?.submit(); }, [launch]);
  function select(tool: Tool) { if (!selection) return; const next = { ...selection, tool }; const query = new URLSearchParams(window.location.search); query.set('tool', tool); window.history.replaceState(null, '', `/workspace?${query}`); setSelection(next); }
  return <main className="workspace-page">
    <header className="workspace-toolbar"><div className="workspace-leading"><a id="back-console" href={`/console${selection?.project ? `?project=${encodeURIComponent(selection.project)}` : ''}`}>Back to console</a><div className="workspace-title"><strong id="workspace-project">{projectName}</strong><small>Developer workspace</small></div></div>
      <div className="workspace-actions"><div className="workspace-switcher"><label htmlFor="workspace-tool">Open in</label><Select id="workspace-tool" value={selection?.tool || 'workbench'} onChange={event => select(event.target.value as Tool)} disabled={loading}>{[['workbench','JupyterLab'],['ide','Browser IDE'],['langflow','Langflow canvas']].map(([id,name]) => <option key={id} value={id} disabled={id !== selection?.tool && id !== 'langflow' && !workspaces.find(item => item.id === id)?.available}>{name}</option>)}</Select></div><span id="workspace-status" className="workspace-status" role="status">{error ? 'Workspace unavailable' : loading ? 'Opening workspace…' : `${selection?.tool === 'langflow' ? 'Langflow' : 'Workspace'} signed in with Kionga`}</span><a className="workspace-logout" href="/auth/logout">Log out</a></div>
    </header>
    {(loading || error) && <div className="workspace-loading"><p className={error ? 'feedback error' : 'workspace-message'} id="workspace-message" role={error ? 'alert' : 'status'}>{error || 'Preparing your workspace…'}</p>{error && <button id="workspace-retry" type="button" onClick={() => setAttempt(value => value + 1)}>Retry</button>}</div>}
    <iframe id="workspace-frame" className="workspace-frame" title="Developer workspace" name="kionga-isolated-workspace" src={launch && !launch.ticket ? launch.url : undefined} hidden={!launch} allow="clipboard-read; clipboard-write" />
    {launch?.ticket && <form ref={handoff} method="post" action={launch.url} target="kionga-isolated-workspace" hidden><input type="hidden" name="ticket" value={launch.ticket} /><input type="hidden" name="next" value={launch.next || ''} /></form>}
  </main>;
}
