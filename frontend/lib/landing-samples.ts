/** Small, deterministic examples for the public workbench. No service calls. */
export const SAMPLE_CSV = "actual,probability\n1,0.92\n0,0.12\n1,0.73\n0,0.34\n1,0.48\n0,0.68\n1,0.81\n0,0.09";

export function evaluateSample(csv: string, threshold: number) {
  if (!Number.isFinite(threshold) || threshold < 0 || threshold > 1) throw new Error("Choose a threshold between 0 and 1.");
  const lines = csv.trim().split(/\r?\n/);
  if (lines.shift()?.trim() !== "actual,probability") throw new Error("Use the columns actual,probability.");
  if (!lines.length || lines.length > 1000) throw new Error("Enter between 1 and 1,000 sample rows.");
  const matrix = { truePositive: 0, falsePositive: 0, trueNegative: 0, falseNegative: 0 };
  const rows = lines.map((line, index) => {
    const cells = line.split(",").map(cell => cell.trim());
    const [actual, probability] = cells.map(Number);
    if (cells.length !== 2 || cells.some(cell => !cell) || ![0, 1].includes(actual) || !Number.isFinite(probability) || probability < 0 || probability > 1) throw new Error(`Row ${index + 2}: use a binary label and a probability between 0 and 1.`);
    const predicted = probability >= threshold ? 1 : 0;
    matrix[actual ? (predicted ? "truePositive" : "falseNegative") : (predicted ? "falsePositive" : "trueNegative")]++;
    return { actual, probability, predicted };
  });
  return { rows, matrix, accuracy: (matrix.truePositive + matrix.trueNegative) / rows.length };
}

export const PROJECT_CONFIG = JSON.stringify({ name: "churn-model", estimator: "logistic_regression", jobs: [{ name: "prepare", depends_on: [] }, { name: "train", depends_on: ["prepare"] }, { name: "evaluate", depends_on: ["train"] }] }, null, 2);
type SampleProject = { name: string; estimator: "logistic_regression" | "random_forest"; jobs: {name: string; depends_on: string[]}[] };
export function inspectProject(source: string): SampleProject & { order: string[] } {
  let value: SampleProject;
  try { value = JSON.parse(source); } catch { throw new Error("project.json contains invalid JSON."); }
  if (!value || typeof value !== "object" || !/^[a-z][a-z0-9-]{0,47}$/.test(value.name || "")) throw new Error("Use a project name with lowercase letters, numbers and hyphens.");
  if (!["logistic_regression", "random_forest"].includes(value.estimator)) throw new Error("Choose logistic_regression or random_forest.");
  if (!Array.isArray(value.jobs) || !value.jobs.length || value.jobs.length > 20) throw new Error("Define between 1 and 20 jobs.");
  const names = value.jobs.map(job => job?.name);
  if (names.some(name => typeof name !== "string" || !/^[a-z][a-z0-9_-]{0,31}$/.test(name)) || new Set(names).size !== names.length) throw new Error("Give every job a unique lowercase name.");
  if (value.jobs.some(job => !Array.isArray(job.depends_on) || job.depends_on.some(dep => !names.includes(dep)))) throw new Error("Every dependency must name a job in this project.");
  const order: string[] = [];
  for (let pass = 0; pass < names.length; pass++) for (const job of value.jobs) if (!order.includes(job.name) && job.depends_on.every(dep => order.includes(dep))) order.push(job.name);
  if (order.length !== names.length) throw new Error("Dependencies contain a cycle. Remove a connection and check again.");
  return { ...value, order };
}
export function projectFiles(project: SampleProject) {
  const estimator = project.estimator === "random_forest" ? "RandomForestClassifier" : "LogisticRegression";
  const module = project.estimator === "random_forest" ? "ensemble" : "linear_model";
  return {
    "train.py": `from pathlib import Path\nimport pandas as pd\nfrom sklearn.${module} import ${estimator}\n\ndata = pd.read_csv(Path("data/train.csv"))\nX = data.drop(columns=["target"])\ny = data["target"]\nmodel = ${estimator}(random_state=42)\nmodel.fit(X, y)\n`,
    "pipeline.yaml": `# Dependency outline; configure execution images in your project.\nname: ${project.name}\njobs:\n${project.jobs.map(job => `  - name: ${job.name}\n    depends_on: [${job.depends_on.join(", ")}]`).join("\n")}\n`,
  };
}
export function renderSamplePrompt(template: string, input: string) {
  if (!template.includes("{input}")) throw new Error("Include {input} in the prompt template.");
  if (!input.trim()) throw new Error("Enter an incident to pass into the flow.");
  if (input.length + template.length > 12000) throw new Error("Keep the sample input and template below 12,000 characters.");
  return template.replaceAll("{input}", () => input);
}
export function routeSample(input: string, keyword: string) {
  if (!input.trim() || !keyword.trim()) throw new Error("Enter a request and a routing keyword.");
  const matched = input.toLowerCase().includes(keyword.trim().toLowerCase());
  return { matched, path: ["input", "condition", matched ? "review" : "answer", "output"], output: matched ? "Route to human review." : "Route to the answer workflow." };
}
