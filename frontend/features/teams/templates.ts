// Starter teams. The JSON files are byte-for-byte copies of contracts agent-team/templates.
// Applying one creates any missing agents and opens an unsaved team, so nothing runs until a
// person reviews and saves it.
import codeChange from "./templates/code-change.json";
import research from "./templates/research.json";
import { definitionsApi } from "./api";
import { blankDefinition, blankTeam } from "./validation";
import type { AgentDefinition, Team, TeamLimits, ToolGrant, DefinitionLimits, Workspace } from "./types";

export type TeamTemplate = {
  id: string;
  name: string;
  description: string;
  team: {
    name: string;
    description: string;
    coordinator: { instructions: string; model: string };
    members: string[];
    brief: string;
    limits: TeamLimits;
  };
  agents: Array<{
    name: string;
    description: string;
    instructions: string;
    model: string;
    tools: ToolGrant[];
    workspace: Workspace;
    limits: DefinitionLimits;
  }>;
  setup_notes: string[];
};
export const TEAM_TEMPLATES = [codeChange, research] as TeamTemplate[];
/** Reuses agents that already have the template's name, so a retry after a failure is safe. */
export async function applyTemplate(
  template: TeamTemplate,
  project: string,
  existing: AgentDefinition[],
): Promise<Team> {
  const ids = new Map<string, string>();
  for (const agent of template.agents) {
    const found = existing.find((item) => item.project_id === project && item.name === agent.name);
    const saved = found || (await definitionsApi.create({ ...blankDefinition(project), ...agent }));
    ids.set(agent.name, saved.id!);
  }
  return {
    ...blankTeam(project),
    ...template.team,
    members: template.team.members.map((name) => ids.get(name)!),
  };
}
