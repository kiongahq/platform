"use client";
import {Select} from "@/components/ui/Select";
import {CodeEditor} from "@/components/ui/CodeEditor";
import { useEffect, useRef, useState } from "react";
import { request, errorMessage } from "@/lib/api";
import { useQuery } from "@/hooks/useQuery";
import { items, type Entity } from "@/lib/types";
import {
  ActionForm,
  DataState,
  Detail,
  Feedback,
  ResourceTable,
  Status,
  type Field,
} from "@/features/shared/View";
import { AgentOperations } from "@/features/agents/AgentsView";
import type { GraphDraft } from "./graph";
export function DesignStudio({
  draft,
  dirty,
  writable,
  validation,
  onValidate,
  onIssueNode,
  onSettingsChange,
  onNodeStatus,
}: {
  draft: GraphDraft;
  dirty: boolean;
  writable: boolean;
  validation: Entity | null;
  onValidate: () => Promise<unknown>;
  onIssueNode: (id: string) => void;
  onSettingsChange: (settings: Entity) => void;
  onNodeStatus: (id: string, status: string) => void;
}) {
  const [tab, setTab] = useState("playground"),
    [error, setError] = useState(""),
    [message, setMessage] = useState(""),
    [run, setRun] = useState<Entity | null>(null),
    [events, setEvents] = useState<Entity[]>([]),
    [turns, setTurns] = useState<Array<{ role: string; text: string }>>([]),
    [delta, setDelta] = useState(""),
    [last, setLast] = useState<{ input: string; output: string } | null>(null),
    [history, setHistory] = useState<Entity[]>([]),
    [session, setSession] = useState(""),
    [caseDraft, setCaseDraft] = useState<Entity | null>(null),
    [evaluation, setEvaluation] = useState<Entity | null>(null),
    [compare, setCompare] = useState<string[]>([]),
    [comparison, setComparison] = useState<Entity[] | null>(null),
    [opsAgent, setOpsAgent] = useState(""),
    [requireEvaluation, setRequireEvaluation] = useState(true);
  const casesPanel = useRef<HTMLDivElement>(null);
  const path = draft.id
    ? `/api/v1/agent-designs/${encodeURIComponent(draft.id)}`
    : null;
  const fixtures = useQuery<Entity>(path ? `${path}/fixtures` : null),
    evaluations = useQuery<Entity>(path ? `${path}/evaluations` : null),
    versions = useQuery<Entity>(path ? `${path}/versions` : null),
    agents = useQuery<Entity>("/api/v1/agents");
  const overview = useQuery<Entity>(
    `/api/v1/agent-designs/overview${draft.project_id ? `?project_id=${encodeURIComponent(draft.project_id)}` : ""}`,
  );
  const ready = !!draft.id && !dirty,
    abort = useRef<AbortController | null>(null),
    lastInput = useRef("");
  useEffect(() => {
    setEvents([]);
    setTurns([]);
    setRun(null);
    setHistory([]);
    setSession("");
    setEvaluation(null);
    setCompare([]);
    setComparison(null);
    setCaseDraft(null);
    setLast(null);
    setOpsAgent("");
    setError("");
    setMessage("");
  }, [draft.id]);
  useEffect(() => {
    if (fixtures.data) setCaseDraft(structuredClone(fixtures.data));
  }, [fixtures.data]);
  useEffect(() => {
    if (!run || !path) return;
    const controller = new AbortController();
    abort.current = controller;
    let after = -1,
      finished = false,
      source: EventSource | null = null,
      timer: ReturnType<typeof setTimeout> | undefined;
    const done = () => {
      if (finished) return;
      finished = true;
      source?.close();
      clearTimeout(timer);
      setRun(null);
      evaluations.reload();
    };
    const handle = (event: Entity) => {
      if (controller.signal.aborted) return;
      if (event.seq !== undefined) after = Math.max(after, event.seq);
      setEvents((old) =>
        old.some((item) => item.seq !== undefined && item.seq === event.seq)
          ? old
          : [...old, event],
      );
      const status = (
        {
          "node.started": "running",
          "node.retrying": "retrying",
          "node.completed": "succeeded",
          "node.failed": "failed",
          "node.skipped": "skipped",
        } as Record<string, string>
      )[event.type];
      if (event.node_id && status) onNodeStatus(event.node_id, status);
      if (event.type === "run.completed") {
        setMessage(
          `Succeeded${event.duration_ms !== undefined ? ` in ${event.duration_ms} ms` : ""}.`,
        );
        const output = event.output || "";
        setDelta("");
        setTurns((old) => [
          ...old,
          { role: "Agent", text: output || "(empty reply)" },
        ]);
        setLast({ input: lastInput.current, output });
        setHistory((old) =>
          [
            ...old,
            { role: "user", text: lastInput.current },
            { role: "assistant", text: output },
          ].slice(-50),
        );
      }
      if (event.type === "run.failed" || event.type === "run.cancelled") {
        setError(event.error?.message || "The run was cancelled.");
        setDelta("");
      }
    };
    const eventsPath = `${path}/runs/${encodeURIComponent(run.id)}/events`;
    const poll = async () => {
      if (finished || controller.signal.aborted) return;
      try {
        const result = await request<Entity>(`${eventsPath}?after=${after}`, {
          signal: controller.signal,
        });
        for (const event of result.events || []) handle(event);
        if (result.done) {
          done();
          return;
        }
      } catch (e) {
        if (!controller.signal.aborted) setError(errorMessage(e));
      }
      if (!finished) timer = setTimeout(poll, 500);
    };
    if (typeof EventSource === "function") {
      source = new EventSource(eventsPath);
      source.addEventListener("run", (event) => {
        try {
          handle(JSON.parse((event as MessageEvent).data));
        } catch {
          /* malformed event */
        }
      });
      source.addEventListener("delta", (event) => {
        try {
          const value = JSON.parse((event as MessageEvent).data);
          if (value.delta) setDelta((old) => old + value.delta);
        } catch {
          /* malformed delta */
        }
      });
      source.addEventListener("end", done);
      source.onerror = () => {
        source?.close();
        void poll();
      };
    } else void poll();
    return () => {
      controller.abort();
      source?.close();
      clearTimeout(timer);
    };
  }, [run?.id, path]);
  useEffect(() => {
    if (
      !evaluation ||
      ["completed", "failed", "cancelled"].includes(evaluation.status)
    )
      return;
    const controller = new AbortController();
    const timer = setInterval(() => {
      void request<Entity>(`/api/v1/agent-evaluations/${evaluation.id}`, {
        signal: controller.signal,
      })
        .then((value) => {
          setEvaluation(value);
          if (value.status === "completed") {
            evaluations.reload();
            setMessage(
              `Evaluation completed: ${value.passed ? "passed" : "failed"} quality gate.`,
            );
          }
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(errorMessage(e));
        });
    }, 1000);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [evaluation?.id, evaluation?.status]);
  async function execute(task: () => Promise<any>) {
    setError("");
    try {
      return await task();
    } catch (e) {
      setError(errorMessage(e));
      return null;
    }
  }
  async function start(values: Entity) {
    if (!ready) throw new Error("Save your changes before running a design.");
    const sessionID = session || `play-${crypto.randomUUID()}`;
    setSession(sessionID);
    lastInput.current = values.input;
    setEvents([]);
    onNodeStatus("", "");
    setDelta("");
    const { version_id, ...body } = values;
    const value = await request<Entity>(
      version_id ? `/api/v1/agent-versions/${version_id}/runs` : `${path}/runs`,
      {
        method: "POST",
        body: {
          ...body,
          session_id: sessionID,
          history,
          revision: draft.revision,
        },
        headers: { "Idempotency-Key": `ui-${crypto.randomUUID()}` },
      },
    );
    setTurns((old) => [...old, { role: "You", text: values.input }]);
    setRun(value);
  }
  const tabs = [
    "playground",
    "validation",
    "tests",
    "evaluations",
    "versions",
    "operations",
    "settings",
  ];
  const pending = events.filter(
    (event) =>
      event.type === "approval.requested" &&
      !events.some(
        (resolved) =>
          resolved.type === "approval.resolved" &&
          (resolved.approval_id || resolved.approval?.id) ===
            (event.approval_id || event.approval?.id),
      ),
  );
  return (
    <section className="designer-studio">
      <dl className="stat-row" id="design-kpis">
        {[
          ["Designs", "designs"],
          ["Published versions", "versions"],
          ["Deployments", "deployments"],
          ["Runs", "runs"],
          ["Success rate", "success_rate"],
          ["P95 latency (ms)", "p95_latency_ms"],
          ["Measured cost", "cost_usd"],
        ].map(([label, key]) => (
          <div key={key}>
            <dt>{label}</dt>
            <dd>
              {overview.data?.[key] === null ||
              overview.data?.[key] === undefined
                ? "Unavailable"
                : key === "success_rate"
                  ? percent(overview.data[key])
                  : overview.data[key]}
            </dd>
          </div>
        ))}
      </dl>
      <div className="tabs" role="tablist" aria-label="Agent studio panels">
        {tabs.map((value) => (
          <button
            key={value}
            role="tab"
            id={`design-tab-${value}`}
            data-design-tab={value}
            aria-selected={tab === value}
            aria-controls={`design-panel-${value}`}
            onClick={() => setTab(value)}
          >
            {value[0].toUpperCase() + value.slice(1)}
          </button>
        ))}
      </div>
      <Feedback error={error} message={message} />
      {!ready && tab !== "validation" && tab !== "settings" && (
        <p className="feedback">
          Save the design to run, evaluate or publish its current revision.
        </p>
      )}
      <div
        id={`design-panel-${tab}`}
        role="tabpanel"
        aria-labelledby={`design-tab-${tab}`}
        data-design-panel={tab}
      >
        {tab === "playground" && (
          <>
            <div
              className="chat-log"
              id="design-chat-log"
              role="log"
              aria-live="polite"
            >
              {turns.map((turn, i) => (
                <div key={i}>
                  <b>{turn.role}</b>
                  <p>{turn.text}</p>
                </div>
              ))}
              {delta && (
                <div>
                  <b>Agent</b>
                  <p>{delta}</p>
                </div>
              )}
            </div>
            <ActionForm
              fields={
                [
                  {
                    name: "input",
                    label: "Message",
                    type: "textarea",
                    required: true,
                  },
                  {
                    name: "version_id",
                    label: "Run target",
                    type: "select",
                    options: [
                      { value: "", label: `Saved revision ${draft.revision}` },
                      ...items(versions.data).map((v) => ({
                        value: v.id,
                        label: `Published version ${v.version}`,
                      })),
                    ],
                    value: "",
                  },
                  {
                    name: "stop_after",
                    label: "Stop after node",
                    type: "select",
                    options: [
                      { value: "", label: "Run to completion" },
                      ...draft.nodes.map((node) => ({
                        value: node.id,
                        label: node.name,
                      })),
                    ],
                    value: "",
                  },
                  {
                    name: "allow_side_effects",
                    label: "Allow side effects",
                    type: "checkbox",
                  },
                ].map((field) => ({
                  ...field,
                  disabled: !ready || !writable || !!run,
                })) as Field[]
              }
              submit="Run agent"
              onSubmit={start}
            />
            {run && (
              <button
                onClick={() =>
                  void execute(() =>
                    request(`${path}/runs/${run.id}/cancel`, {
                      method: "POST",
                      body: {},
                    }),
                  )
                }
              >
                Cancel run
              </button>
            )}
            {last && (
              <button
                disabled={!ready || !writable}
                onClick={() => {
                  const current = caseDraft || { revision: 0, cases: [] };
                  setCaseDraft({
                    ...current,
                    cases: [
                      ...(current.cases || []),
                      {
                        id: `case-${crypto.randomUUID().slice(0, 8)}`,
                        input: last.input,
                        expected: {
                          contains: last.output
                            .split(/\s+/)
                            .filter((v) => v.length > 4)
                            .slice(0, 3),
                        },
                      },
                    ],
                  });
                  setTab("tests");
                }}
              >
                Save last run as test case
              </button>
            )}
            {pending.map((event) => {
              const approval = event.approval || event,
                id = event.approval_id || approval.id;
              return (
                <section key={id} className="feedback">
                  <h3>Approval required</h3>
                  <p>{approval.message || approval.prompt || event.node_id}</p>
                  <Detail value={approval} />
                  <ActionForm
                    fields={[
                      {
                        name: "decision",
                        label: "Decision",
                        type: "select",
                        options: ["approve", "reject"],
                        value: "approve",
                        disabled: !writable,
                      },
                      { name: "comment", label: "Comment" },
                    ]}
                    submit="Submit decision"
                    onSubmit={async (body) => {
                      const result = await request<Entity>(
                        `/api/v1/agent-approvals/${id}/decision`,
                        { method: "POST", body },
                      );
                      setEvents((old) => [
                        ...old,
                        {
                          type: "approval.resolved",
                          approval_id: id,
                          approval: result,
                        },
                      ]);
                    }}
                  />
                </section>
              );
            })}
            <h3>Run trace</h3>
            <ResourceTable
              rows={events}
              columns={[
                { key: "seq", label: "Sequence" },
                { key: "type", label: "Event" },
                { key: "node_id", label: "Node" },
                { key: "duration_ms", label: "Duration (ms)" },
              ]}
              actions={(event) => <Detail value={event} />}
            />
          </>
        )}
        {tab === "validation" && (
          <>
            <button id="design-validate" onClick={() => void onValidate()}>
              Validate graph
            </button>
            {validation ? (
              <>
                <p role="status">
                  {validation.runnable ? "Runnable" : "Not runnable"}
                </p>
                <ul>
                  {(validation.issues || []).map((issue: Entity, i: number) => (
                    <li key={i}>
                      <Status value={issue.severity} /> {issue.message}{" "}
                      {issue.node_id && (
                        <button onClick={() => onIssueNode(issue.node_id)}>
                          Show node
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
                {validation.content_sha256 && (
                  <code>{validation.content_sha256}</code>
                )}
              </>
            ) : (
              <p>
                Validation checks connections, resource assignments, settings
                and node configuration.
              </p>
            )}
          </>
        )}
        {tab === "tests" && (
          <div ref={casesPanel}><DataState
            loading={fixtures.loading}
            error={fixtures.error}
            onRetry={fixtures.reload}
          >
            <p>
              Test cases define expected outputs, tool calls, latency limits and
              optional judge criteria.
            </p>
            {(caseDraft?.cases || []).map((item: Entity, index: number) => (
              <CaseEditor
                key={index}
                item={item}
                writable={writable}
                onChange={(value) =>
                  setCaseDraft({
                    ...caseDraft,
                    cases: caseDraft!.cases.map((old: Entity, i: number) =>
                      i === index ? value : old,
                    ),
                  })
                }
                onRemove={() =>
                  setCaseDraft({
                    ...caseDraft,
                    cases: caseDraft!.cases.filter(
                      (_: Entity, i: number) => i !== index,
                    ),
                  })
                }
              />
            ))}
            <div className="button-row">
              <button
                disabled={!ready || !writable}
                onClick={() =>
                  setCaseDraft({
                    ...caseDraft,
                    cases: [
                      ...(caseDraft?.cases || []),
                      {
                        id: `case-${crypto.randomUUID().slice(0, 8)}`,
                        input: "",
                        expected: { contains: [] },
                      },
                    ],
                  })
                }
              >
                Add test case
              </button>
              <button
                className="primary"
                disabled={!ready || !writable}
                onClick={() =>
                  void execute(async () => {
                    const invalid = [...(casesPanel.current?.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input,textarea") || [])].find(control => !control.reportValidity());
                    if (invalid) throw new Error("Correct the invalid test case fields before saving.");
                    const saved = await request<Entity>(`${path}/fixtures`, {
                      method: "PUT",
                      body: {
                        revision: caseDraft?.revision || 0,
                        cases: caseDraft?.cases || [],
                      },
                    });
                    setCaseDraft(saved);
                    setMessage("Test cases saved.");
                    fixtures.reload();
                  })
                }
              >
                Save test cases
              </button>
            </div>
          </DataState></div>
        )}
        {tab === "evaluations" && (
          <>
            <ActionForm
              fields={
                [
                  {
                    name: "version_id",
                    label: "Evaluate",
                    type: "select",
                    options: [
                      { value: "", label: `Saved revision ${draft.revision}` },
                      ...items(versions.data).map((v) => ({
                        value: v.id,
                        label: `Published v${v.version}`,
                      })),
                    ],
                    value: "",
                  },
                  {
                    name: "gate",
                    label: "Quality gate (%)",
                    type: "number",
                    value: 80,
                    min: 0,
                    max: 100,
                  },
                  {
                    name: "allow_side_effects",
                    label: "Allow side effects",
                    type: "checkbox",
                  },
                ].map((field) => ({
                  ...field,
                  disabled: !ready || !writable,
                })) as Field[]
              }
              submit="Run evaluation"
              onSubmit={async (values) => {
                const item = await request<Entity>(`${path}/evaluations`, {
                  method: "POST",
                  body: {
                    ...values,
                    gate: values.gate / 100,
                    revision: draft.revision,
                  },
                });
                setEvaluation(item);
                evaluations.reload();
              }}
            />
            <DataState
              loading={evaluations.loading}
              error={evaluations.error}
              onRetry={evaluations.reload}
            >
              <ResourceTable
                rows={items(evaluations.data)}
                columns={[
                  {
                    key: "id",
                    label: "Evaluation",
                    render: (item) => (
                      <label>
                        <input
                          type="checkbox"
                          aria-label={`Compare evaluation ${item.id}`}
                          checked={compare.includes(item.id)}
                          disabled={item.status !== "completed"}
                          onChange={(e) =>
                            setCompare((old) =>
                              e.target.checked
                                ? [...old, item.id].slice(-2)
                                : old.filter((id) => id !== item.id),
                            )
                          }
                        />
                        {item.id}
                      </label>
                    ),
                  },
                  { key: "status", label: "Status" },
                  {
                    key: "pass_rate",
                    label: "Pass rate",
                    render: (item) => percent(item.pass_rate),
                  },
                  {
                    key: "gate",
                    label: "Gate",
                    render: (item) =>
                      `${percent(item.gate)} ${item.passed ? "passed" : "failed"}`,
                  },
                  { key: "p95_latency_ms", label: "P95 (ms)" },
                  { key: "cost_usd", label: "Cost" },
                  {
                    key: "judge_scores",
                    label: "Judge scores",
                    render: (item) => JSON.stringify(item.judge_scores || {}),
                  },
                  {
                    key: "failure_reasons",
                    label: "Failure reasons",
                    render: (item) =>
                      JSON.stringify(item.failure_reasons || {}),
                  },
                ]}
                actions={(item) => (
                  <button
                    onClick={() =>
                      void execute(async () =>
                        setEvaluation(
                          await request<Entity>(
                            `/api/v1/agent-evaluations/${item.id}`,
                          ),
                        ),
                      )
                    }
                  >
                    View
                  </button>
                )}
              />
            </DataState>
            {compare.length === 2 && (
              <button
                onClick={() =>
                  void execute(async () =>
                    setComparison(
                      await Promise.all(
                        compare.map((id) =>
                          request<Entity>(`/api/v1/agent-evaluations/${id}`),
                        ),
                      ),
                    ),
                  )
                }
              >
                Compare selected
              </button>
            )}
            {comparison && (
              <section>
                <h3>Evaluation comparison</h3>
                <ResourceTable
                  rows={comparison}
                  columns={[
                    { key: "id", label: "Evaluation" },
                    {
                      key: "pass_rate",
                      label: "Pass rate",
                      render: (item) => percent(item.pass_rate),
                    },
                    { key: "p95_latency_ms", label: "P95 (ms)" },
                    { key: "cost_usd", label: "Cost" },
                  ]}
                />
                <ResourceTable
                  rows={items({ items: comparison[1].cases })}
                  columns={[
                    { key: "id", label: "Case" },
                    {
                      key: "before",
                      label: "Before",
                      render: (item) =>
                        String(
                          (comparison[0].cases || []).find(
                            (old: Entity) => old.id === item.id,
                          )?.passed ?? "New",
                        ),
                    },
                    {
                      key: "passed",
                      label: "After",
                      render: (item) => String(item.passed),
                    },
                  ]}
                />
              </section>
            )}
            {evaluation && (
              <section>
                <h3>Evaluation {evaluation.id}</h3>
                <p>
                  {evaluation.status} / {evaluation.completed} of{" "}
                  {evaluation.case_count} cases /{" "}
                  {percent(evaluation.pass_rate)} passed
                </p>
                <Detail value={evaluation.slices} />
                <ResourceTable
                  rows={evaluation.cases || []}
                  columns={[
                    { key: "id", label: "Case" },
                    { key: "input", label: "Input" },
                    { key: "output", label: "Output" },
                    {
                      key: "passed",
                      label: "Result",
                      render: (item) => (
                        <Status value={item.passed ? "Passed" : "Failed"} />
                      ),
                    },
                    {
                      key: "failures",
                      label: "Failures",
                      render: (item) => (item.failures || []).join(", "),
                    },
                    {
                      key: "judge",
                      label: "Judge",
                      render: (item) => JSON.stringify(item.judge || {}),
                    },
                  ]}
                  actions={(item) => (
                    <>
                      <Detail value={item} />
                      {writable && (
                        <Select
                          aria-label={`Review case ${item.id}`}
                          value=""
                          onChange={(event) => {
                            const status = event.target.value;
                            if (!status) return;
                            const note = window.prompt(
                              "Review note (optional)",
                            );
                            if (note === null) return;
                            void execute(async () =>
                              setEvaluation(
                                await request<Entity>(
                                  `/api/v1/agent-evaluations/${evaluation.id}/cases/${item.id}/review`,
                                  { method: "POST", body: { status, note } },
                                ),
                              ),
                            );
                          }}
                        >
                          <option value="">Review</option>
                          <option value="accepted">Accept</option>
                          <option value="needs_changes">Needs changes</option>
                          <option value="rejected">Reject</option>
                        </Select>
                      )}
                    </>
                  )}
                />
              </section>
            )}
          </>
        )}
        {tab === "versions" && (
          <>
            <ActionForm
              fields={
                [
                  { name: "notes", label: "Version notes", type: "textarea" },
                  {
                    name: "evaluation_id",
                    label: "Passing evaluation",
                    type: "select",
                    options: [
                      { value: "", label: "No evaluation" },
                      ...items(evaluations.data)
                        .filter((e) => e.passed && e.status === "completed")
                        .map((e) => ({ value: e.id, label: e.id })),
                    ],
                    value: "",
                  },
                ].map((field) => ({
                  ...field,
                  disabled: !ready || !writable,
                })) as Field[]
              }
              submit="Publish version"
              onSubmit={async (body) => {
                const published = await request<Entity>(`${path}/publish`, {
                  method: "POST",
                  body: { ...body, revision: draft.revision },
                });
                versions.reload();
                setMessage(`Published v${published.version}.`);
              }}
            />
            <label className="field"><span><input type="checkbox" checked={requireEvaluation} onChange={event => setRequireEvaluation(event.target.checked)} /> Require a passing evaluation to deploy</span></label>
            <DataState
              loading={versions.loading}
              error={versions.error}
              onRetry={versions.reload}
            >
              <ResourceTable
                rows={items(versions.data)}
                columns={[
                  { key: "version", label: "Version" },
                  { key: "source_revision", label: "Source revision" },
                  { key: "notes", label: "Notes" },
                  { key: "created_at", label: "Published" },
                  { key: "content_sha256", label: "Behavior hash" },
                ]}
                actions={(item) => (
                  <>
                    <button
                      disabled={!writable || !ready}
                      onClick={() => {
                        setTab("playground");
                        setMessage(
                          `Choose published version ${item.version} in the run target.`,
                        );
                      }}
                    >
                      Run version
                    </button>
                    <button
                      disabled={!writable}
                      onClick={() =>
                        void execute(async () => {
                          if (!window.confirm(`Deploy version ${item.version}?`)) return;
                          await request(
                            `/api/v1/agent-versions/${item.id}/deploy`,
                            {
                              method: "POST",
                              body: {
                                require_evaluation: requireEvaluation,
                              },
                            },
                          );
                          setMessage(
                            "Deployment requested. Follow its status in Agents.",
                          );
                          agents.reload();
                        })
                      }
                    >
                      Deploy
                    </button>
                    <Detail value={item} />
                  </>
                )}
              />
            </DataState>
          </>
        )}
        {tab === "operations" && (
          <>
            <label className="field">
              Deployment
              <Select
                value={opsAgent}
                onChange={(e) => setOpsAgent(e.target.value)}
              >
                <option value="">Choose deployment</option>
                {items(agents.data)
                  .filter(
                    (a) =>
                      a.design_id === draft.id ||
                      items(versions.data).some(
                        (v) =>
                          v.id === a.snapshot_id || v.id === a.agent_version_id || v.id === a.version_id,
                      ),
                  )
                  .map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name} {a.version}
                    </option>
                  ))}
              </Select>
            </label>
            {opsAgent ? (
              <AgentOperations
                agent={
                  items(agents.data).find((a) => a.id === opsAgent) || {
                    id: opsAgent,
                  }
                }
                writable={writable}
              />
            ) : (
              <p className="empty-state">
                Choose a deployment to inspect its measured operations and
                canary traffic.
              </p>
            )}
          </>
        )}
        {tab === "settings" && (
          <div className="form-grid">
            {[
              ["max_steps", "Maximum execution steps"],
              ["timeout_seconds", "Timeout (seconds)"],
              ["max_tokens", "Maximum tokens"],
            ].map(([key, label]) => (
              <label className="field" key={key}>
                {label}
                <input
                  type="number"
                  min={0}
                  disabled={!writable}
                  value={draft.settings?.[key] || 0}
                  onChange={(event) =>
                    onSettingsChange({
                      ...draft.settings,
                      [key]: Number(event.target.value),
                    })
                  }
                />
                <small>Zero uses the runtime default.</small>
              </label>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}
function percent(value: any) {
  return typeof value === "number"
    ? `${Math.round(value * 1000) / 10}%`
    : "Unavailable";
}
function CaseEditor({
  item,
  writable,
  onChange,
  onRemove,
}: {
  item: Entity;
  writable: boolean;
  onChange: (value: Entity) => void;
  onRemove: () => void;
}) {
  const expected = item.expected || {},
    csv = (value: string) =>
      value
        .split(",")
        .map((v) => v.trim())
        .filter(Boolean);
  return (
    <fieldset className="test-case">
      <legend>{item.id || "Test case"}</legend>
      <div className="form-grid">
        <label className="field">
          Case ID
          <input
            value={item.id}
            disabled={!writable}
            onChange={(e) => onChange({ ...item, id: e.target.value })}
          />
        </label>
        <label className="field">
          Tags
          <input
            value={(item.tags || []).join(", ")}
            disabled={!writable}
            onChange={(e) => onChange({ ...item, tags: csv(e.target.value) })}
          />
        </label>
        <label className="field field-wide">
          Input
          <textarea
            value={item.input}
            disabled={!writable}
            onChange={(e) => onChange({ ...item, input: e.target.value })}
          />
        </label>
        {[
          ["contains", "Contains"],
          ["not_contains", "Must not contain"],
          ["calls", "Required node calls"],
        ].map(([key, label]) => (
          <label className="field" key={key}>
            {label}
            <input
              value={(expected[key] || []).join(", ")}
              disabled={!writable}
              onChange={(e) =>
                onChange({
                  ...item,
                  expected: { ...expected, [key]: csv(e.target.value) },
                })
              }
            />
          </label>
        ))}
        {[
          ["equals", "Exact output"],
          ["matches", "Output pattern"],
        ].map(([key, label]) => (
          <label className="field" key={key}>
            {label}
            <input
              value={expected[key] || ""}
              disabled={!writable}
              onChange={(e) =>
                onChange({
                  ...item,
                  expected: { ...expected, [key]: e.target.value },
                })
              }
            />
          </label>
        ))}
        <label className="field">
          Maximum latency (ms)
          <input
            type="number"
            min={0}
            value={expected.max_latency_ms || 0}
            disabled={!writable}
            onChange={(e) =>
              onChange({
                ...item,
                expected: {
                  ...expected,
                  max_latency_ms: Number(e.target.value),
                },
              })
            }
          />
        </label>
        <div className="field"><span className="field-caption">Judge criteria (JSON)</span><CodeEditor language="json" label="Judge criteria (JSON)"
            defaultValue={JSON.stringify(expected.judge || {}, null, 2)}
            disabled={!writable}
            onChange={(e) => {
              try { const value = JSON.parse(e.target.value); if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(); e.target.setCustomValidity(""); }
              catch { e.target.setCustomValidity("Enter a JSON object."); }
            }}
            onBlur={(e) => {
              try {
                const judge = JSON.parse(e.target.value);
                if (!judge || typeof judge !== "object" || Array.isArray(judge)) throw new Error();
                onChange({
                  ...item,
                  expected: {
                    ...expected,
                    judge: Object.keys(judge).length ? judge : undefined,
                  },
                });
                e.target.setCustomValidity("");
              } catch {
                e.target.setCustomValidity("Enter valid JSON.");
                e.target.reportValidity();
              }
            }}
          />
        </div>
      </div>
      <button type="button" disabled={!writable} onClick={onRemove}>
        Remove case
      </button>
    </fieldset>
  );
}
