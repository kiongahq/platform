"use client";
import {Select} from "@/components/ui/Select";
import {CodeEditor} from "@/components/ui/CodeEditor";
import { useEffect, useState } from "react";
import { request, errorMessage } from "@/lib/api";
import { useQuery } from "@/hooks/useQuery";
import { items, hasService, type Entity } from "@/lib/types";
import {
  ActionForm,
  DataState,
  Detail,
  Feedback,
  Modal,
  ResourceTable,
  Status,
  type ViewProps,
  type Field,
} from "@/features/shared/View";
export function agentContractPayload(document: Entity) {
  if (!document || typeof document !== 'object' || Array.isArray(document)) throw new Error('The agent contract must be a JSON object.');
  if (document.kind !== 'KiongaAgent') return document;
  const spec = document.spec || {}, resources = spec.resources?.requests || {}, gpu = Object.entries(resources).find(([key]) => !['cpu','memory'].includes(key));
  return { name: document.metadata?.name || '', version: spec.version || '', image: spec.image || '', graph_module: spec.graphModule || '', llm_backend: spec.llm?.backend || 'mock', autoscaling:{min_replicas:spec.replicas?.min || 1,max_replicas:spec.replicas?.max || 1}, resources:{cpu:resources.cpu || '500m',memory:resources.memory || '1Gi',gpu:Number(gpu?.[1] || 0),gpu_type:gpu?.[0] || 'nvidia.com/gpu'}, tools:(spec.tools || []).map((tool:any)=>typeof tool === 'string' ? tool : tool.name).filter(Boolean) };
}
export function buildAgentDeployment(values: Entity, identity?: Entity, existing: Entity[] = []) {
  const min = Number(values.min_replicas), max = Number(values.max_replicas), gpu = Number(values.gpu || 0);
  if (!Number.isInteger(min) || !Number.isInteger(max) || min < 1 || max < min) throw new Error('Maximum replicas must be at least the positive minimum replicas.');
  if (!Number.isInteger(gpu) || gpu < 0) throw new Error('GPUs per replica must be a non-negative whole number.');
  const grant = identity?.roles?.some((role:string)=>['admin','operator'].includes(role)) ? null : identity?.entitlements;
  if (grant) {
    const owned = existing.filter(agent=>!agent.owner_subject || agent.owner_subject === identity?.subject);
    const replicas = (agent:Entity)=>Number(agent.autoscaling?.max_replicas || agent.replicas || 1);
    if (owned.reduce((sum,agent)=>sum+replicas(agent),0)+max > Number(grant.compute?.max_vms || 0)) throw new Error('Maximum replicas exceed your assigned workload capacity, including existing deployments.');
    if (owned.reduce((sum,agent)=>sum+Number(agent.resources?.gpu || 0)*replicas(agent),0)+gpu*max > Number(grant.compute?.gpus || 0)) throw new Error('GPU capacity at maximum replicas exceeds your assignment.');
    if (gpu > 0 && grant.compute?.gpu_type && values.gpu_type !== grant.compute.gpu_type) throw new Error('GPU resource does not match your assigned GPU type.');
  }
  if (!Array.isArray(values.tools) || values.tools.some((tool:unknown)=>typeof tool !== 'string')) throw new Error('Tools must be a list of registered tool names.');
  return { project_id:String(values.project_id || ''),name:String(values.name || '').trim(),version:String(values.version || '').trim(),image:String(values.image || '').trim(),graph_module:String(values.graph_module || '').trim(),llm_backend:String(values.llm_backend || 'mock'),replicas:min,autoscaling:{min_replicas:min,max_replicas:max},resources:{cpu:String(values.cpu || '500m'),memory:String(values.memory || '1Gi'),gpu,gpu_type:String(values.gpu_type || 'nvidia.com/gpu')},tools:[...new Set(values.tools)] };
}
export function AgentsView({ identity, project, resource, onNavigate }: ViewProps) {
  const agents = useQuery<Entity>("/api/v1/agents"),
    projects = useQuery<Entity>("/api/v1/project-options"),
    tools = useQuery<Entity>(
      hasService(identity, "catalog") ? "/api/v1/tools" : null,
    ),
    prompts = useQuery<Entity>(
      hasService(identity, "catalog") ? "/api/v1/prompts" : null,
    );
  const [telemetryLoading,setTelemetryLoading]=useState(false), [telemetryUnavailable,setTelemetryUnavailable]=useState(0);
  const [sessions, setSessions] = useState<Entity[]>([]),
    [traces, setTraces] = useState<Entity[]>([]),
    [metrics, setMetrics] = useState<Entity>({}),
    [dialog, setDialog] = useState<{ kind: string; item?: Entity } | null>(
      null,
    ),
    [error, setError] = useState("");
  const list = items(agents.data).filter(
      (item) => !project || item.project_id === project,
    ),
    writable = identity.permissions.agents_write === true;
  useEffect(() => {
    const abort = new AbortController();
    if (!agents.data) return;
    setTelemetryLoading(true);
    Promise.all(
      list.map(async (agent) => {
        const get = (suffix: string) =>
          request<Entity>(`/api/v1/agents/${agent.id}/${suffix}`, {
            signal: abort.signal,
          }).catch(() => ({ items: [], available: false }));
        return {
          agent,
          ...Object.fromEntries(
            await Promise.all(
              ["sessions", "traces", "metrics"].map(async (key) => [
                key,
                await get(key),
              ]),
            ),
          ),
        };
      }),
    ).then((groups) => {
      if (abort.signal.aborted) return;
      setTelemetryLoading(false);
      setTelemetryUnavailable(groups.filter(g=>g.sessions.available===false || g.traces.available===false).length);
      setSessions(groups.flatMap((g) => items(g.sessions)));
      setTraces(
        groups
          .flatMap((g) => items(g.traces))
          .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)),
      );
      setMetrics(
        Object.fromEntries(groups.map((g) => [g.agent.id, g.metrics])),
      );
    });
    return () => abort.abort();
  }, [agents.data, project]);
  const measured = sessions.filter((item) => item.usage_available !== false),
    unmetered = sessions.length - measured.length;
  const projectOptions = items(projects.data).map((item) => ({
    value: item.id,
    label: item.name,
  }));
  const detail = list.find((item) => item.id === resource);
  return (
    <section aria-label="Agent deployments">
      <div className="toolbar">
        <p>Deployments, conversations and measured runtime usage.</p>
        <div className="button-row">
          <button onClick={agents.reload}>Refresh</button>
          {writable && (
            <button
              className="primary"
              id="deploy-agent"
              onClick={() => setDialog({ kind: "deploy" })}
            >
              Deploy agent
            </button>
          )}
        </div>
      </div>
      <Feedback error={error} />
      {telemetryLoading && <p role="status">Loading runtime sessions and traces…</p>}
      {telemetryUnavailable > 0 && <p role="status" className="feedback">Sessions or traces are unavailable for {telemetryUnavailable} deployments. The totals below include only returned records.</p>}
      <dl className="stat-row">
        <div>
          <dt>Agent versions</dt>
          <dd>{list.length}</dd>
        </div>
        <div>
          <dt>Active sessions</dt>
          <dd>{sessions.filter((item) => item.status === "running").length}</dd>
        </div>
        <div>
          <dt>Measured cost</dt>
          <dd>
            $
            {measured
              .reduce((sum, item) => sum + Number(item.cost_usd || 0), 0)
              .toFixed(4)}
            {unmetered ? " + unmetered sessions" : ""}
          </dd>
        </div>
      </dl>
      <DataState
        loading={agents.loading}
        error={agents.error}
        onRetry={agents.reload}
      >
        <ResourceTable
          rows={list}
          columns={[
            {
              key: "name",
              label: "Agent",
              render: (item) => (
                <button
                  className="text-button"
                  onClick={() => onNavigate("agents", item.id)}
                >
                  {item.name}
                </button>
              ),
            },
            { key: "version", label: "Version" },
            {
              key: "status",
              label: "Status",
              render: (item) => <Status value={item.status} />,
            },
            {
              key: "graph_module",
              label: "Entrypoint",
              render: (item) => <code>{item.graph_module}</code>,
            },
            {
              key: "usage",
              label: "Resource usage",
              render: (item) =>
                metrics[item.id]?.available
                  ? `${metrics[item.id].cpu_millicores || 0}m CPU / ${(Number(metrics[item.id].memory_bytes || 0) / 1048576).toFixed(0)} MiB / ${metrics[item.id].pods || 0} pods`
                  : "Unavailable",
            },
            {
              key: "canary_weight",
              label: "Canary",
              render: (item) => `${item.canary_weight ?? 0}%`,
            },
          ]}
          actions={(item) => (
            <>
              {writable && (
                <>
                  <button onClick={() => setDialog({ kind: "traffic", item })}>
                    Traffic
                  </button>
                  <button
                    disabled={item.status !== "ready"}
                    onClick={() => setDialog({ kind: "chat", item })}
                  >
                    Chat
                  </button>
                </>
              )}
              <button onClick={() => setDialog({ kind: "operations", item })}>
                Operations
              </button>
            </>
          )}
        />
      </DataState>
      {detail && <AgentDetails agent={detail} />}
      <section>
        <h2>Sessions</h2>
        <ResourceTable
          rows={sessions}
          columns={[
            { key: "id", label: "Session" },
            { key: "agent_id", label: "Agent" },
            { key: "current_node", label: "Node" },
            { key: "status", label: "Status" },
            { key: "turns", label: "Turns" },
            {
              key: "tokens",
              label: "Tokens",
              render: (item) =>
                item.usage_available === false
                  ? "Unavailable"
                  : Number(item.input_tokens || 0) +
                    Number(item.output_tokens || 0),
            },
            {
              key: "cost_usd",
              label: "Cost",
              render: (item) =>
                item.usage_available === false
                  ? "Unavailable"
                  : `$${Number(item.cost_usd || 0).toFixed(4)}`,
            },
          ]}
          actions={(item) => <Detail value={item} />}
        />
      </section>
      <section>
        <h2>Recent traces</h2>
        <ResourceTable
          rows={traces.slice(0, 30)}
          columns={[
            { key: "created_at", label: "Time" },
            { key: "agent_id", label: "Agent" },
            { key: "status", label: "Status" },
            { key: "duration_ms", label: "Duration (ms)" },
            {
              key: "tokens", label: "Tokens", render: (item) => item.metadata?.usage_available === false ? "Unavailable" : (item.tokens ?? "Unavailable"),
            },
            { key:"components", label:"Component timing", render:(item)=>{ const components=item.metadata?.lfx_timing?.component_timings; return Array.isArray(components) && components.length ? <details><summary>{components.length} components</summary><ul>{components.map((part:Entity,index:number)=><li key={index}>{part.component || 'Component'}: {Number(part.duration || 0).toFixed(3)} s</li>)}</ul></details> : 'Unavailable'; } },
          ]}
          actions={(item) => <Detail value={item} />}
        />
      </section>
      {hasService(identity, "catalog") && (
        <section>
          <h2>Prompt catalog</h2>
          <DataState
            loading={prompts.loading}
            error={prompts.error}
            onRetry={prompts.reload}
          >
            {prompts.data?.configured ? (
              <ResourceTable
                rows={items(prompts.data)}
                columns={[
                  { key: "name", label: "Prompt" },
                  { key: "version", label: "Version" },
                  {
                    key: "labels",
                    label: "Labels",
                    render: (item) => (item.labels || []).join(", "),
                  },
                ]}
              />
            ) : (
              <p className="empty-state">
                Configure Langfuse in Platform to use the prompt catalog.
              </p>
            )}
          </DataState>
        </section>
      )}
      {dialog && (
        <Modal
          title={
            dialog.kind === "deploy"
              ? "Deploy agent"
              : dialog.kind === "traffic"
                ? "Canary traffic"
                : dialog.kind === "chat"
                  ? `Chat with ${dialog.item?.name}`
                  : dialog.kind === "operations"
                    ? "Agent operations"
                    : "Agent details"
          }
          onClose={() => setDialog(null)}
        >
          {dialog.kind === "deploy" ? (
            <DeployAgentForm identity={identity} project={project} projects={items(projects.data)} tools={items(tools.data)} toolError={tools.error} existing={items(agents.data)} onDeployed={() => { setDialog(null); agents.reload(); }} />
          ) : dialog.kind === "traffic" ? (
            <ActionForm
              fields={[
                {
                  name: "canary_weight",
                  label: "Canary weight (%)",
                  type: "number",
                  value: dialog.item?.canary_weight || 0,
                  min: 0,
                  max: 100,
                },
              ]}
              submit="Update traffic"
              onSubmit={async (body) => {
                await request(`/api/v1/agents/${dialog.item?.id}/traffic`, {
                  method: "PUT",
                  body,
                });
                setDialog(null);
                agents.reload();
              }}
            />
          ) : dialog.kind === "chat" ? (
            <AgentChat agent={dialog.item!} onInvoked={agents.reload} />
          ) : dialog.kind === "operations" ? (
            <AgentOperations agent={dialog.item!} writable={writable} />
          ) : (
            <AgentDetails agent={dialog.item!} />
          )}
        </Modal>
      )}
    </section>
  );
}
export function DeployAgentForm({identity,project,projects,tools,toolError,existing,onDeployed}:{identity:Entity;project:string;projects:Entity[];tools:Entity[];toolError?:string;existing:Entity[];onDeployed:()=>void}) {
  const grant=identity.roles.some((role:string)=>['admin','operator'].includes(role)) ? null : identity.entitlements;
  const [framework,setFramework]=useState('langgraph'),[contractText,setContractText]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const [values,setValues]=useState<Entity>({project_id:project || projects[0]?.id || '',name:'',version:'1.0.0',image:'',graph_module:'agents.customer_support.graph:build',llm_backend:'mock',min_replicas:1,max_replicas:1,cpu:'500m',memory:'1Gi',gpu:0,gpu_type:grant?.compute?.gpu_type || 'nvidia.com/gpu',tools:[]});
  const update=(key:string,value:unknown)=>setValues(old=>({...old,[key]:value}));
  const toolChoices=[...tools,...(values.tools || []).filter((name:string)=>!tools.some(tool=>tool.name===name)).map((name:string)=>({name,description:'Imported tool name'}))];
  const reserved=existing.filter(agent=>!agent.owner_subject || agent.owner_subject===identity.subject).reduce((sum,agent)=>sum+Number(agent.autoscaling?.max_replicas || agent.replicas || 1),0);
  function loadContract() {
    setError('');
    try {
      if(!contractText.trim())throw new Error('Paste the generated platform/agent.json contract first.');
      const document=agentContractPayload(JSON.parse(contractText));
      const fields:Entity={};
      for(const key of ['project_id','name','version','image','graph_module','llm_backend','tools'])if(document[key]!==undefined)fields[key]=document[key];
      if(document.autoscaling || document.replicas) {fields.min_replicas=document.autoscaling?.min_replicas ?? document.replicas;fields.max_replicas=document.autoscaling?.max_replicas ?? document.replicas;}
      for(const key of ['cpu','memory','gpu','gpu_type'])if(document.resources?.[key]!==undefined)fields[key]=document.resources[key];
      if(fields.tools && (!Array.isArray(fields.tools)||fields.tools.some((tool:unknown)=>typeof tool!=='string')))throw new Error('The contract tools must be a list of tool names.');
      setValues(old=>({...old,...fields}));
    }catch(failure){setError(errorMessage(failure));}
  }
  async function deploy(event:React.FormEvent<HTMLFormElement>) {
    event.preventDefault();setError('');setBusy(true);
    try {
      if(!projects.some(item=>item.id===values.project_id))throw new Error('Select an assigned project before deploying an agent.');
      const payload=buildAgentDeployment(values,identity,existing);
      await request('/api/v1/agents',{method:'POST',body:payload});onDeployed();
    }catch(failure){setError(errorMessage(failure));}finally{setBusy(false);}
  }
  function field(key:string,label:string,type='text',required=false,min?:number,max?:number){return <label className="field" key={key}>{label}<input name={key} type={type} value={values[key]} required={required} disabled={busy} min={min} max={max} onChange={event=>update(key,event.target.value)}/></label>;}
  return <><label className="field">Framework<Select value={framework} disabled={busy} onChange={event=>{const kind=event.target.value;setFramework(kind);update('graph_module',kind==='langgraph'?'agents.customer_support.graph:build':`agents.framework_examples:build_${kind}`);}}><option value="langgraph">LangGraph</option><option value="agno">Agno</option><option value="nooa">NOOA</option><option value="custom">Custom adapter</option></Select></label>
    <p className="feedback">{framework==='nooa'?'NOOA runs model-generated code, so the runtime refuses to start it until an isolated code sandbox is available. No environment variable enables it. Choose LangGraph, Agno or a custom adapter instead.':framework==='agno'?'Build with the Agno extra and configure AGNO_MODEL and provider credentials in the deployment. The LangGraph model selector does not configure Agno.':framework==='custom'?'The adapter factory owns provider configuration and durable session storage.':'LangGraph uses platform model and checkpointer settings.'}</p>
    <details><summary>Import agent contract</summary><div className="field"><span className="field-caption">platform/agent.json</span><CodeEditor language="json" label="platform/agent.json" value={contractText} onChange={event=>setContractText(event.target.value)} rows={6}/></div><button type="button" disabled={busy} onClick={loadContract}>Load contract</button></details>
    <form id="deploy-agent-form" onSubmit={deploy} aria-busy={busy}><div className="form-grid"><label className="field">Project<Select name="project_id" value={values.project_id} required disabled={busy} onChange={event=>update('project_id',event.target.value)}>{projects.map(item=><option key={item.id} value={item.id}>{item.name}</option>)}</Select></label>{field('name','Name','text',true)}{field('version','Version','text',true)}{field('image','Image','text',true)}{field('graph_module','Graph factory','text',true)}<label className="field">Model backend<Select name="llm_backend" value={values.llm_backend} disabled={busy} onChange={event=>update('llm_backend',event.target.value)}>{['mock','openai','anthropic','vllm'].map(value=><option key={value} value={value}>{value}</option>)}</Select></label>{field('min_replicas','Minimum replicas','number',true,1,grant?.compute?.max_vms)}{field('max_replicas','Maximum replicas','number',true,1,grant?.compute?.max_vms)}{field('cpu','CPU per replica','text',true)}{field('memory','Memory per replica','text',true)}{field('gpu','GPUs per replica','number',true,0,grant?.compute?.gpus)}{field('gpu_type','GPU resource')}</div>
      <p className="field-help" id="agent-capacity-hint">{grant?`Assigned: ${grant.compute?.vcpus || 0} vCPU, ${grant.compute?.memory_gb || 0} GB memory, ${grant.compute?.gpus || 0} GPUs and ${grant.compute?.max_vms || 0} maximum replicas. ${reserved} replicas are already reserved. Admission includes 100m CPU and 128Mi memory per trace sidecar.`:'Capacity is checked against the administrator’s grant by the gateway.'}</p>
      <fieldset><legend>Registered tools</legend>{toolError && <p className="feedback">The tool catalog is unavailable. Imported tool selections are retained.</p>}{toolChoices.length?<div className="checkbox-list">{toolChoices.map(tool=><label key={tool.name}><input type="checkbox" name="agent_tools" value={tool.name} checked={values.tools.includes(tool.name)} disabled={busy} onChange={event=>update('tools',event.target.checked?[...values.tools,tool.name]:values.tools.filter((name:string)=>name!==tool.name))}/><span>{tool.name}<small>{tool.description || 'Registered platform tool'}{tool.version?` (version ${tool.version})`:''}</small></span></label>)}</div>:<p className="field-help">No catalog tools are available. The agent can be deployed without tools.</p>}</fieldset>
      <Feedback error={error}/>{!projects.length && <p role="alert">Ask your administrator to assign a project before deploying an agent.</p>}<button type="submit" className="primary" disabled={busy || !projects.length}>{busy?'Deploying…':'Deploy agent'}</button>
    </form></>;
}
function AgentDetails({agent}:{agent:Entity}) { return <section className="resource-detail"><h2>{agent.name} {agent.version}</h2><dl className="facts">{[['Agent ID',agent.id],['Project',agent.project_id],['Status',agent.status],['Image',agent.image],['Graph factory',agent.graph_module],['Model backend',agent.llm_backend],['Canary',`${agent.canary_weight ?? 0}%`],['Tools',(agent.tools||[]).join(', ') || 'None']].map(([label,value])=><div key={label}><dt>{label}</dt><dd>{value || 'Unavailable'}</dd></div>)}</dl><Detail value={agent}/></section>; }
function AgentChat({agent,onInvoked}:{agent:Entity;onInvoked:()=>void}) {
  const [turns,setTurns]=useState<Array<{role:string;text:string;meta?:string}>>([]),[session,setSession]=useState(''),[message,setMessage]=useState(''),[busy,setBusy]=useState(false),[error,setError]=useState('');
  async function send(event:React.FormEvent<HTMLFormElement>){event.preventDefault();const text=message.trim();if(!text||busy)return;setBusy(true);setMessage('');setError('');setTurns(old=>[...old,{role:'You',text}]);try{const reply=await request<Entity>(`/api/v1/agents/${encodeURIComponent(agent.id)}/invoke`,{method:'POST',body:{message:text,session_id:session,user_id:'console'}});setSession(reply.session_id||session);setTurns(old=>[...old,{role:agent.name,text:String(reply.reply||reply.output||''),meta:`${reply.usage_available===false?'Usage unavailable':`${Number(reply.input_tokens||0)+Number(reply.output_tokens||0)} tokens`}; ${reply.duration_ms ?? 'unavailable'} ms`}]);onInvoked();}catch(failure){const text=errorMessage(failure);setError(text);setTurns(old=>[...old,{role:agent.name,text}]);}finally{setBusy(false);}}
  return <><div className="chat-log" role="log" aria-live="polite">{!turns.length&&<p className="field-help">Send a message through the agent runtime. Each turn is traced.</p>}{turns.map((turn,index)=><article key={index}><strong>{turn.role}</strong><p>{turn.text}</p>{turn.meta&&<small>{turn.meta}</small>}</article>)}{busy&&<p role="status">Agent is responding…</p>}</div><form onSubmit={send} aria-busy={busy}><label className="field">Message<textarea value={message} onChange={event=>setMessage(event.target.value)} disabled={busy} required/></label><Feedback error={error}/><button type="submit" disabled={busy||!message.trim()}>{busy?'Sending…':'Send'}</button></form></>;
}
export function AgentOperations({agent,writable=false}:{agent:Entity;writable?:boolean}) {
  const [hours,setHours]=useState('24'),[weight,setWeight]=useState(String(agent.canary_weight ?? 0)),[busy,setBusy]=useState(false),[error,setError]=useState('');
  const query=useQuery<Entity>(`/api/v1/agents/${encodeURIComponent(agent.id)}/operations?hours=${hours}`), data=query.data, totals=data?.totals || {}, series=items(data?.series), siblings=items(data?.siblings);
  useEffect(()=>setWeight(String(agent.canary_weight ?? 0)),[agent.id,agent.canary_weight]);
  const numeric=(value:unknown, digits=0)=>value===null||value===undefined?'Unavailable':Number(value).toLocaleString(undefined,{maximumFractionDigits:digits});
  async function traffic(event:React.FormEvent<HTMLFormElement>){event.preventDefault();const value=Number(weight);if(!Number.isInteger(value)||value<0||value>100){setError('Canary weight must be a whole percentage from 0 to 100.');return;}setBusy(true);setError('');try{await request(`/api/v1/agents/${encodeURIComponent(agent.id)}/traffic`,{method:'PUT',body:{canary_weight:value}});query.reload();}catch(failure){setError(errorMessage(failure));}finally{setBusy(false);}}
  return <section aria-label={`${agent.name} operations`} className="stack"><div className="toolbar"><h3>Runtime operations</h3><label className="field">Observation window<Select value={hours} onChange={event=>setHours(event.target.value)}>{[1,6,24,72,168].map(value=><option key={value} value={value}>{value} hours</option>)}</Select></label><button onClick={query.reload}>Refresh operations</button></div><DataState loading={query.loading} error={query.error} onRetry={query.reload}>{data&&<><dl className="stat-row">{[['Requests',numeric(totals.requests)],['Errors',numeric(totals.errors)],['Success rate',data.success_rate==null?'Unavailable':`${(Number(data.success_rate)*100).toFixed(1)}%`],['p95 latency',totals.p95_ms==null?'Unavailable':`${numeric(totals.p95_ms,1)} ms`],['Measured tokens',numeric(Number(totals.input_tokens||0)+Number(totals.output_tokens||0))],['Measured cost',totals.cost_usd==null?'Unavailable':`$${Number(totals.cost_usd).toFixed(4)}`]].map(([label,value])=><div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl><p className="field-help">Usage totals include recorded measurements. Unavailable provider usage is not estimated.</p><div className="operations-charts">{[{title:'Requests',fields:['requests','errors']},{title:'Latency (ms)',fields:['p50_ms','p95_ms','p99_ms']},{title:'Tokens',fields:['input_tokens','output_tokens']},{title:'Cost (USD)',fields:['cost_usd']}].map(chart=><SeriesChart key={chart.title} title={chart.title} series={series} fields={chart.fields}/>)}</div><details><summary>Recorded time buckets</summary><ResourceTable rows={series.map((item,index)=>({...item,id:String(index)}))} columns={[{key:'start',label:'Bucket start'},{key:'requests',label:'Requests'},{key:'errors',label:'Errors'},{key:'p95_ms',label:'p95 (ms)',render:item=>numeric(item.p95_ms,1)},{key:'input_tokens',label:'Input tokens'},{key:'output_tokens',label:'Output tokens'},{key:'cost_usd',label:'Cost (USD)',render:item=>numeric(item.cost_usd,6)}]}/></details><h4>Version traffic</h4><ResourceTable rows={siblings} columns={[{key:'version',label:'Version'},{key:'id',label:'Agent ID'},{key:'canary_weight',label:'Canary weight',render:item=>`${item.canary_weight ?? 0}%`},{key:'created_at',label:'Created'}]}/><h4>Recent traces</h4><ResourceTable rows={items(data.recent_traces)} columns={[{key:'id',label:'Trace'},{key:'created_at',label:'Time'},{key:'status',label:'Status',render:item=><Status value={item.status}/>},{key:'duration_ms',label:'Duration (ms)'},{key:'error',label:'Error'}]}/><h4>Rollout timeline</h4><ResourceTable rows={items(data.timeline).map((item,index)=>({...item,id:String(index)}))} columns={[{key:'at',label:'Time'},{key:'action',label:'Action'},{key:'actor',label:'Actor'},{key:'version',label:'Version'},{key:'details',label:'Details',render:item=>typeof item.details==='object'?JSON.stringify(item.details):item.details}]}/></>}</DataState>{writable&&<form onSubmit={traffic} className="action-form" aria-busy={busy}><label className="field">Canary weight (%)<input type="number" min="0" max="100" step="1" required value={weight} onChange={event=>setWeight(event.target.value)} disabled={busy}/></label><p className="field-help">Weight applies to this version within the agent’s version group.</p><Feedback error={error}/><button type="submit" disabled={busy}>{busy?'Updating traffic…':'Update canary traffic'}</button></form>}</section>;
}
function SeriesChart({title,series,fields}:{title:string;series:Entity[];fields:string[]}) {
  const max=Math.max(1,...series.flatMap(bucket=>fields.map(field=>Number(bucket[field]||0)))),labels=fields.map(field=>field.replaceAll('_',' '));
  return <figure className="panel series-chart"><figcaption>{title}</figcaption>{series.length?<><svg viewBox="0 0 480 140" role="img" aria-label={`${title} across ${series.length} recorded time buckets`}><path d="M20 10V120H470" fill="none" stroke="var(--color-rule)"/>{fields.map((field,index)=><g key={field}>{series.map((bucket,i)=>bucket[field]==null?null:<circle key={i} cx={20+(series.length===1?225:i/(series.length-1)*450)} cy={120-Number(bucket[field])/max*100} r="2" fill={index===0?'var(--color-accent)':index===1?'var(--color-muted)':'var(--color-ink)'}><title>{bucket.start}: {field} {bucket[field]}</title></circle>)}<polyline fill="none" stroke={index===0?'var(--color-accent)':index===1?'var(--color-muted)':'var(--color-ink)'} strokeWidth="2" strokeDasharray={index===0?undefined:index===1?'6 3':'2 3'} points={series.flatMap((bucket,i)=>bucket[field]==null?[]:[`${20+(series.length===1?225:i/(series.length-1)*450)},${120-Number(bucket[field])/max*100}`]).join(' ')}/></g>)}</svg><p className="field-help">{labels.join(' · ')}. Peak scale: {max.toLocaleString(undefined,{maximumFractionDigits:6})}.</p></>:<p className="empty-state">No recorded time buckets in this window.</p>}</figure>;
}
