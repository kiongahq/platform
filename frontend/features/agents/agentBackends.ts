// The model backends the gateway accepts for an agent. This list must equal
// store.AgentLLMBackends in go/internal/store/workloads.go; a Go test reads this
// file and fails when the two differ. Labels equal the values so the console,
// the API and the docs use the same words.
export const AGENT_LLM_BACKENDS = [
  { value: 'mock', label: 'mock' },
  { value: 'openai', label: 'openai' },
  { value: 'anthropic', label: 'anthropic' },
  { value: 'openai-compatible', label: 'openai-compatible' },
  { value: 'self-hosted', label: 'self-hosted' },
] as const;

export type AgentLLMBackend = (typeof AGENT_LLM_BACKENDS)[number]['value'];

export const DEFAULT_AGENT_LLM_BACKEND: AgentLLMBackend = 'mock';
