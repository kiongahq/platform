// Same rules as the gateway (build spec section 2), so an editor can point at the exact
// field before saving. The gateway still validates every request.
import type { AgentDefinition, Team, ToolGrant } from "./types";

export type FieldErrors = Record<string, string>;
export const NAME_PATTERN = /^[a-z][a-z0-9-]{0,62}$/;
export const DEFINITION_LIMITS = {
  max_turns: { label: "Maximum turns", min: 1, max: 500 },
  max_tokens: { label: "Maximum tokens", min: 1, max: 5_000_000 },
  max_seconds: { label: "Maximum seconds", min: 60, max: 14_400 },
} as const;
export const TEAM_LIMITS = {
  max_workers_total: { label: "Total workers", min: 1, max: 50 },
  max_concurrent_workers: { label: "Concurrent workers", min: 1, max: 16 },
  max_tokens: { label: "Maximum tokens", min: 1, max: 20_000_000 },
  max_seconds: { label: "Maximum seconds", min: 60, max: 86_400 },
} as const;
export const TEXT_LIMITS = {
  description: 1000,
  instructions: 32_000,
  brief: 16_000,
  task: 32_000,
};
const MAX_MEMBERS = 16;
const length = (value: string) => [...value].length;

function checkLimits(
  errors: FieldErrors,
  values: Record<string, number>,
  rules: Record<string, { label: string; min: number; max: number }>,
) {
  for (const [key, rule] of Object.entries(rules)) {
    const value = values[key];
    if (!Number.isInteger(value) || value < rule.min || value > rule.max)
      errors[`limits.${key}`] =
        `${rule.label} must be a whole number from ${rule.min.toLocaleString("en")} to ${rule.max.toLocaleString("en")}.`;
  }
}
function checkText(
  errors: FieldErrors,
  key: string,
  value: string,
  label: string,
  max: number,
  required = true,
) {
  if (required && !value.trim()) errors[key] = `Enter ${label}.`;
  else if (length(value) > max)
    errors[key] =
      `${label[0].toUpperCase()}${label.slice(1)} must be at most ${max.toLocaleString("en")} characters.`;
}
// Same pattern as contracts agent-team/agent-definition.v1: no user information, query or fragment.
const REPOSITORY_PATTERN = /^https:\/\/[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?(\/[A-Za-z0-9_~+-][A-Za-z0-9._~+-]*)+\/?$/;
export function repositoryError(value: string) {
  if (!value.trim()) return "Enter the repository URL.";
  return REPOSITORY_PATTERN.test(value.trim()) && value.trim().length <= 2048
    ? ""
    : "Use an https:// repository URL with no credentials, query or fragment.";
}
export function validateDefinition(draft: AgentDefinition): FieldErrors {
  const errors: FieldErrors = {};
  if (!draft.project_id) errors.project_id = "Select a project.";
  if (!NAME_PATTERN.test(draft.name))
    errors.name =
      "Use a lowercase letter first, then lowercase letters, digits or hyphens, up to 63 characters.";
  checkText(errors, "description", draft.description, "when to use this agent", TEXT_LIMITS.description);
  checkText(errors, "instructions", draft.instructions, "instructions", TEXT_LIMITS.instructions);
  if (!draft.model.trim() || length(draft.model) > 256)
    errors.model = "Enter a model name of at most 256 characters.";
  if (draft.workspace === "none") {
    if (draft.tools.some((tool) => tool.kind === "sandbox" || tool.kind === "git"))
      errors.workspace =
        "Code sandbox and git access need the sandbox workspace.";
  }
  draft.tools.forEach((tool, index) => {
    if (tool.kind !== "git") return;
    const problem = repositoryError(tool.repository);
    if (problem) errors[`tools.${index}.repository`] = problem;
  });
  checkLimits(errors, draft.limits, DEFINITION_LIMITS);
  return errors;
}
export function validateTeam(draft: Team): FieldErrors {
  const errors: FieldErrors = {};
  if (!draft.project_id) errors.project_id = "Select a project.";
  if (!NAME_PATTERN.test(draft.name))
    errors.name =
      "Use a lowercase letter first, then lowercase letters, digits or hyphens, up to 63 characters.";
  checkText(errors, "description", draft.description, "a description", TEXT_LIMITS.description, false);
  checkText(errors, "coordinator.instructions", draft.coordinator.instructions, "coordinator instructions", TEXT_LIMITS.instructions);
  if (!draft.coordinator.model.trim() || length(draft.coordinator.model) > 256)
    errors["coordinator.model"] = "Enter a model name of at most 256 characters.";
  if (draft.members.length < 1 || draft.members.length > MAX_MEMBERS)
    errors.members = `Select from 1 to ${MAX_MEMBERS} members.`;
  checkText(errors, "brief", draft.brief, "the shared brief", TEXT_LIMITS.brief, false);
  checkLimits(errors, draft.limits, TEAM_LIMITS);
  return errors;
}
const grantKey = (tool: ToolGrant) =>
  tool.kind === "sandbox"
    ? "sandbox"
    : tool.kind === "git"
      ? `git:${tool.repository.trim()}`
      : `${tool.kind}:${tool.ref}`;
/**
 * The request body. The contract is closed and its update body omits every server-assigned
 * field, including project_id, so a stored document is never echoed back. Repeated grants are removed.
 */
export function definitionPayload(draft: AgentDefinition, update = false): AgentDefinition {
  const seen = new Set<string>();
  return {
    ...(update ? {} : { project_id: draft.project_id }),
    name: draft.name.trim(),
    description: draft.description.trim(),
    instructions: draft.instructions,
    model: draft.model.trim(),
    tools: draft.tools
      .map((tool) =>
        tool.kind === "git"
          ? {
              kind: "git" as const,
              repository: tool.repository.trim(),
              ...(tool.connection ? { connection: tool.connection } : {}),
            }
          : tool,
      )
      .filter((tool) => {
        const key = grantKey(tool);
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      }),
    workspace: draft.workspace,
    limits: { ...draft.limits },
  };
}
export function teamPayload(draft: Team, update = false): Team {
  return {
    ...(update ? {} : { project_id: draft.project_id }),
    name: draft.name.trim(),
    description: draft.description.trim(),
    coordinator: {
      instructions: draft.coordinator.instructions,
      model: draft.coordinator.model.trim(),
    },
    members: [...new Set(draft.members)],
    brief: draft.brief,
    limits: { ...draft.limits },
  };
}
export const blankDefinition = (project = ""): AgentDefinition => ({
  project_id: project,
  name: "",
  description: "",
  instructions: "",
  model: "llm",
  tools: [],
  workspace: "none",
  limits: { max_turns: 100, max_tokens: 200_000, max_seconds: 3600 },
});
export const blankTeam = (project = ""): Team => ({
  project_id: project,
  name: "",
  description: "",
  coordinator: { instructions: "", model: "llm" },
  members: [],
  brief: "",
  limits: {
    max_workers_total: 20,
    max_concurrent_workers: 5,
    max_tokens: 2_000_000,
    max_seconds: 14_400,
  },
});
