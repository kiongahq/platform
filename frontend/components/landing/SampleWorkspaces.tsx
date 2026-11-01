"use client";
import {useRef, useState, type KeyboardEvent, type ReactNode} from "react";
import Link from "next/link";
import {CodeBlock} from "@/components/ui/CodeBlock";
import {CodeEditor} from "@/components/ui/CodeEditor";
import {SAMPLE_CSV, PROJECT_CONFIG, evaluateSample, inspectProject, projectFiles, renderSamplePrompt, routeSample} from "@/lib/landing-samples";

const tools = [
  {id: "notebook", title: "JupyterLab", file: "evaluate.ipynb", href: "/workspace?tool=workbench", action: "Open JupyterLab"},
  {id: "ide", title: "Browser IDE", file: "churn-model/", href: "/workspace?tool=ide", action: "Open browser IDE"},
  {id: "langflow", title: "Langflow", file: "incident-summary", href: "/console/langflow", action: "Open Langflow"},
  {id: "designer", title: "Agent designer", file: "request-router", href: "/console/agent-designer", action: "Open agent designer"},
];
function Result({children}: {children: ReactNode}) {return <div className="sample-result" role="status">{children}</div>;}
function Source({children}: {children: string}) {return <CodeBlock className="sample-code-view" format={false}>{children}</CodeBlock>;}
function download(name: string, source: string, type = "text/plain") {
  const url = URL.createObjectURL(new Blob([source], {type}));
  const link = document.createElement("a"); link.href = url; link.download = name; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function NotebookSample() {
  const [csv, setCSV] = useState(SAMPLE_CSV), [threshold, setThreshold] = useState(.5);
  const [result, setResult] = useState<ReturnType<typeof evaluateSample> | null>(null), [error, setError] = useState("");
  const source = `import pandas as pd\nfrom sklearn.metrics import accuracy_score\n\nrows = pd.read_csv("sample.csv")\npredicted = (rows.probability >= ${threshold.toFixed(2)}).astype(int)\naccuracy_score(rows.actual, predicted)`;
  return <div className="sample-columns">
    <div className="sample-inputs"><h3>Evaluate a classifier.</h3><p>Edit the sample labels and probabilities, then run the cell.</p>
      <label>Sample data (CSV)<textarea className="sample-data" value={csv} spellCheck={false} onChange={event => {setCSV(event.target.value); setResult(null);}} /></label>
      <label>Decision threshold <output>{threshold.toFixed(2)}</output><input type="range" min="0" max="1" step="0.05" value={threshold} onChange={event => {setThreshold(Number(event.target.value)); setResult(null);}} /></label>
      <div className="button-row"><button className="primary" onClick={() => {try {setResult(evaluateSample(csv, threshold)); setError("");} catch (failure) {setError((failure as Error).message); setResult(null);}}}>Run cell</button><button onClick={() => {setCSV(SAMPLE_CSV); setThreshold(.5); setResult(null); setError("");}}>Reset data</button></div>
      {error && <p role="alert" className="feedback error">{error}</p>}
    </div>
    <div className="sample-output"><div className="sample-file-heading"><code>evaluate.ipynb</code><span>Python example</span></div><Source>{source}</Source>
      {result ? <Result><div className="sample-score"><span>Sample accuracy</span><strong>{(result.accuracy * 100).toFixed(1)}%</strong><span>{result.rows.length} rows evaluated</span></div><dl className="sample-matrix"><div><dt>True positive</dt><dd>{result.matrix.truePositive}</dd></div><div><dt>False positive</dt><dd>{result.matrix.falsePositive}</dd></div><div><dt>True negative</dt><dd>{result.matrix.trueNegative}</dd></div><div><dt>False negative</dt><dd>{result.matrix.falseNegative}</dd></div></dl></Result> : <p className="sample-output-hint">Run the cell to inspect the confusion matrix.</p>}
      <p className="sample-footnote">This sample calculates in your browser. Open JupyterLab for a Python kernel and your project data.</p>
    </div>
  </div>;
}

function IDESample() {
  const [config, setConfig] = useState(PROJECT_CONFIG), [file, setFile] = useState("project.json");
  const [project, setProject] = useState(() => inspectProject(PROJECT_CONFIG)), [error, setError] = useState(""), [checked, setChecked] = useState(false);
  const files = projectFiles(project);
  const content = file === "project.json" ? config : files[file as keyof typeof files];
  return <div className="sample-columns">
    <div className="sample-inputs"><h3>Inspect a project before it runs.</h3><p>Change the estimator or job dependencies in the project file. Check it to update the generated files.</p>
      <nav className="sample-files" aria-label="Sample project files">{["project.json", "train.py", "pipeline.yaml"].map(name => <button key={name} aria-pressed={file === name} onClick={() => setFile(name)}><code>{name}</code></button>)}</nav>
      <div className="sample-dependencies" aria-label="Last checked job order">{project.order.map((name, index) => <span key={name}>{index > 0 && <span aria-hidden="true"> / </span>}<code>{name}</code></span>)}</div>
      <p className="sample-footnote">Checks cover names, supported estimators, missing dependencies and cycles.</p>
    </div>
    <div className="sample-output"><div className="sample-file-heading"><code>{file}</code><span>{file === "project.json" ? "Editable" : "Generated from checked config"}</span></div>
      {file === "project.json" ? <CodeEditor label="Sample project configuration" filename="project.json" language="json" className="sample-code-editor" value={config} onChange={event => {setConfig(event.target.value); setChecked(false); setError("");}} /> : <Source>{content}</Source>}
      <div className="button-row"><button className="primary" onClick={() => {try {setProject(inspectProject(config)); setChecked(true); setError("");} catch (failure) {setError((failure as Error).message); setChecked(false);}}}>Check project</button><button onClick={() => download(file, content, file.endsWith(".json") ? "application/json" : "text/plain")}>Download file</button></div>
      {error && <p role="alert" className="feedback error">{error}</p>}
      {checked && <Result>Dependency order: <code>{project.order.join(" / ")}</code>. No cycles found.</Result>}
      <p className="sample-footnote">These files are a local example. Open the IDE to edit your repository and run its tools.</p>
    </div>
  </div>;
}

function FlowNodes({nodes, selected, onSelect, path}: {nodes: {id: string; title: string; detail: string}[]; selected: string; onSelect: (id: string) => void; path?: string[]}) {
  return <div className="sample-flow-nodes" role="group" aria-label="Sample flow nodes">{nodes.map((node, index) => <div className="sample-flow-node" key={node.id}>{index > 0 && <span className="sample-connector" aria-hidden="true">──</span>}<button aria-pressed={selected === node.id} className={path?.includes(node.id) ? "sample-node-visited" : ""} onClick={() => onSelect(node.id)}><strong>{node.title}</strong><span>{node.detail}</span></button></div>)}</div>;
}
const langflowNodes = [{id: "input", title: "Input", detail: "Incident text"}, {id: "prompt", title: "Prompt", detail: "Template"}, {id: "model", title: "Chat model", detail: "Model request"}, {id: "output", title: "Output", detail: "Response"}];
function LangflowSample() {
  const [node, setNode] = useState("prompt"), [input, setInput] = useState("The inference endpoint returns 503 after the latest model deployment.");
  const [template, setTemplate] = useState("Summarize this incident and list the next checks.\n\nIncident: {input}");
  const [rendered, setRendered] = useState<string | null>(null), [error, setError] = useState("");
  return <><h3>See what reaches the model.</h3><p>Select a node, change the prompt and render the request.</p><FlowNodes nodes={langflowNodes} selected={node} onSelect={setNode}/>
    <div className="sample-columns"><div className="sample-inputs"><label>Incident text<textarea value={input} onChange={event => {setInput(event.target.value); setRendered(null);}} /></label>
      <div className="sample-node-inspector"><h4>{langflowNodes.find(item => item.id === node)!.title} configuration</h4>{node === "prompt" ? <label>Prompt template<textarea value={template} onChange={event => {setTemplate(event.target.value); setRendered(null);}} /></label> : node === "input" ? <p>The incident text is passed to the prompt as <code>{"{input}"}</code>.</p> : node === "model" ? <p>The rendered prompt becomes the model request. Configure a provider and credentials inside your Langflow workspace.</p> : <p>The model response is returned through this node. This public sample stops before the model call.</p>}</div>
      <div className="button-row"><button className="primary" onClick={() => {try {setRendered(renderSamplePrompt(template, input)); setError("");} catch (failure) {setError((failure as Error).message); setRendered(null);}}}>Render prompt</button><button onClick={() => setNode("prompt")}>Edit template</button></div>
      {error && <p role="alert" className="feedback error">{error}</p>}
    </div><div className="sample-output"><div className="sample-file-heading"><code>model-request.txt</code><span>Prompt preview</span></div><Source>{rendered || "# Render the prompt to inspect the model input."}</Source>{rendered && <Result>Input substituted. Ready to inspect before a model call.</Result>}<p className="sample-footnote">No model is called here. Open Langflow to connect providers and run the full flow.</p></div></div>
  </>;
}

const agentNodes = [{id: "input", title: "Input", detail: "Customer request"}, {id: "condition", title: "Condition", detail: "Keyword match"}, {id: "review", title: "Review", detail: "Matched branch"}, {id: "answer", title: "Answer", detail: "Default branch"}, {id: "output", title: "Output", detail: "Routing decision"}];
function AgentSample() {
  const [input, setInput] = useState("I need a refund for order 42."), [keyword, setKeyword] = useState("refund"), [node, setNode] = useState("condition");
  const [result, setResult] = useState<ReturnType<typeof routeSample> | null>(null), [error, setError] = useState("");
  const descriptions: Record<string,string> = {input: "Receives the request used by the condition node.", condition: "A case-insensitive match chooses the review branch. All other requests take the answer branch.", review: "A matched request is sent to human review.", answer: "An unmatched request continues to the answer workflow.", output: "Returns the routing decision and the visited node path."};
  return <><h3>Trace a decision through the graph.</h3><p>Change the request or keyword and follow the selected branch.</p>
    <div className="sample-agent-topology"><div className="sample-agent-spine"><FlowNodes nodes={agentNodes.filter(item => ["input", "condition"].includes(item.id))} selected={node} onSelect={setNode} path={result?.path}/></div><div className="sample-agent-branches">{agentNodes.filter(item => ["review", "answer"].includes(item.id)).map(item => <button key={item.id} aria-pressed={node === item.id} className={result?.path.includes(item.id) ? "sample-node-visited" : ""} onClick={() => setNode(item.id)}><strong>{item.title}</strong><span>{item.detail}</span></button>)}</div><div><FlowNodes nodes={agentNodes.filter(item => item.id === "output")} selected={node} onSelect={setNode} path={result?.path}/></div></div>
    <div className="sample-columns"><div className="sample-inputs"><label>Request text<textarea value={input} onChange={event => {setInput(event.target.value); setResult(null);}} /></label><label>Routing keyword<input value={keyword} maxLength={80} onChange={event => {setKeyword(event.target.value); setResult(null);}} /></label><div className="sample-node-inspector"><h4>{agentNodes.find(item => item.id === node)!.title} node</h4><p>{descriptions[node]}</p></div><div className="button-row"><button className="primary" onClick={() => {try {setResult(routeSample(input, keyword)); setError("");} catch (failure) {setError((failure as Error).message); setResult(null);}}}>Run routing sample</button><button onClick={() => {setInput("What are your opening hours?"); setResult(null);}}>Try another request</button></div>{error && <p role="alert" className="feedback error">{error}</p>}</div>
    <div className="sample-output"><div className="sample-file-heading"><code>routing-trace.json</code><span>Local sample</span></div><Source>{result ? JSON.stringify({condition: `contains:${keyword.trim()}`, matched: result.matched, visited: result.path, decision: result.output}, null, 2) : "# Run the sample to inspect the branch and node path."}</Source>{result && <Result>{result.output}</Result>}<p className="sample-footnote">This example executes the routing rule locally. Use the native designer for model calls, tools, approvals and evaluations.</p></div></div>
  </>;
}

export function SampleWorkspaces() {
  const [selected, setSelected] = useState("notebook");
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  function keys(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next = event.key === "Home" ? 0 : event.key === "End" ? tools.length - 1 : event.key === "ArrowRight" ? (index + 1) % tools.length : event.key === "ArrowLeft" ? (index - 1 + tools.length) % tools.length : -1;
    if (next < 0) return; event.preventDefault(); setSelected(tools[next].id); tabs.current[next]?.focus();
  }
  const samples = [<NotebookSample key="notebook"/>, <IDESample key="ide"/>, <LangflowSample key="langflow"/>, <AgentSample key="designer"/>];
  return <section className="landing-section workspace-samples" id="workspaces" aria-labelledby="sample-workspace-title"><div className="section-heading"><div><h2 id="sample-workspace-title">A workspace you can try.</h2><p>Edit inputs. Run a cell. Inspect a prompt. Follow a branch.</p></div><span className="sample-local-label">Sample project</span></div>
    <div className="sample-tool-tabs" role="tablist" aria-label="Sample workspace tools">{tools.map((tool, index) => <button key={tool.id} ref={node => {tabs.current[index] = node;}} id={`workspace-tab-${tool.id}`} role="tab" type="button" tabIndex={selected === tool.id ? 0 : -1} aria-selected={selected === tool.id} aria-controls={`workspace-sample-${tool.id}`} onKeyDown={event => keys(event, index)} onClick={() => setSelected(tool.id)}>{tool.title}</button>)}</div>
    {tools.map((tool, index) => <div className="sample-workbench" data-workspace={tool.id} id={`workspace-sample-${tool.id}`} key={tool.id} role="tabpanel" aria-labelledby={`workspace-tab-${tool.id}`} hidden={selected !== tool.id}><header className="sample-workbench-header"><code>{tool.file}</code><Link href={tool.href}>{tool.action}</Link></header><div className="sample-workbench-body">{samples[index]}</div></div>)}
  </section>;
}
