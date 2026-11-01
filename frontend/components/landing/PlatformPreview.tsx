"use client";
import { useState } from "react";
import Link from "next/link";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { Icon } from "@/components/ui/Icon";
const examples = [
  {
    id: "projects",
    kind: "Project",
    name: "fraud-detection",
    state: "Configured",
    filename: "project.json",
    config: {
      name: "fraud-detection",
      framework: "scikit-learn",
      requested_profile: "starter",
      repository: { default_branch: "main" },
    },
  },
  {
    id: "pipelines",
    kind: "Pipeline",
    name: "train-and-evaluate",
    state: "Draft",
    filename: "pipeline.json",
    config: {
      name: "train-and-evaluate",
      execution_mode: "prefect",
      jobs: [
        { name: "prepare", depends_on: [] },
        { name: "train", depends_on: ["prepare"] },
        { name: "evaluate", depends_on: ["train"] },
      ],
    },
  },
  {
    id: "models",
    kind: "Model",
    name: "churn-classifier",
    state: "Registered",
    filename: "model.json",
    config: {
      name: "churn-classifier",
      version: "v1",
      framework: "scikit-learn",
      artifact_uri: "s3://model-artifacts/churn/v1/",
    },
  },
  {
    id: "agents",
    kind: "Agent",
    name: "support-router",
    state: "Draft",
    filename: "agent.json",
    config: {
      name: "support-router",
      version: "v1",
      llm_backend: "mock",
      autoscaling: { min_replicas: 1, max_replicas: 1 },
      tools: [],
    },
  },
  {
    id: "features",
    kind: "Feature set",
    name: "customer-features",
    state: "Configured",
    filename: "features.json",
    config: {
      name: "customer-features",
      entity: "customer_id",
      features: ["orders_30d", "spend_30d", "days_since_last_order"],
    },
  },
  {
    id: "storage",
    kind: "Storage",
    name: "model-artifacts",
    state: "Configured",
    filename: "storage.json",
    config: {
      name: "model-artifacts",
      type: "s3",
      prefix: "fraud-detection/",
      access: "project-scoped",
    },
  },
];
export function PlatformPreview() {
  const [selected, setSelected] = useState("projects");
  const active = examples.find((item) => item.id === selected)!;
  return (
    <section
      className="landing-section registry-section"
      id="platform"
      aria-labelledby="registry-title"
    >
      <div className="section-heading registry-heading">
        <h2 id="registry-title">
          One project.
          <br />
          Every resource in view.
        </h2>
        <p>
          Inspect a sample resource and its configuration before opening the
          console.
        </p>
      </div>
      <div className="registry-preview">
        <header className="registry-toolbar">
          <strong>fraud-detection</strong>
          <span>Example project</span>
          <Link href="/console/projects">Open your projects</Link>
        </header>
        <div className="registry-content">
          <div
            className="registry-table-wrap"
            tabIndex={0}
            role="region"
            aria-label="Example project resources"
          >
            <table className="registry-table">
              <thead>
                <tr>
                  <th>Resource</th>
                  <th>Kind</th>
                  <th>State</th>
                </tr>
              </thead>
              <tbody>
                {examples.map((item) => (
                  <tr key={item.id} data-selected={selected === item.id}>
                    <td>
                      <button
                        type="button"
                        aria-pressed={selected === item.id}
                        onClick={() => setSelected(item.id)}
                      >
                        <Icon name={item.id} />
                        <code>{item.name}</code>
                      </button>
                    </td>
                    <td>{item.kind}</td>
                    <td>
                      <span
                        className={
                          item.state === "Draft"
                            ? "example-state example-state-draft"
                            : "example-state"
                        }
                      >
                        <Icon
                          name={item.state === "Draft" ? "edit" : "check"}
                        />
                        {item.state}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div
            className="registry-inspector"
            role="region"
            aria-label="Example resource configuration"
          >
            <CodeBlock language="json" filename={active.filename}>
              {JSON.stringify(active.config, null, 2)}
            </CodeBlock>
            <div className="registry-inspector-foot">
              <span>Configuration example</span>
              <Link href={`/console/${active.id}`}>
                Open {active.id === "features" ? "feature stores" : active.id}
              </Link>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
