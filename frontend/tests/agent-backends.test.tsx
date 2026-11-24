import './setup-dom';
import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';
import React from 'react';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/react';
import { DeployAgentForm } from '../features/agents/AgentsView';
import { AGENT_LLM_BACKENDS, DEFAULT_AGENT_LLM_BACKEND } from '../features/agents/agentBackends';

Object.assign(globalThis, { FormData: window.FormData, self: window });
afterEach(cleanup);

// The five values the gateway accepts (go/internal/store AgentLLMBackends). Spelled out
// here on purpose: a Go test keeps agentBackends.ts equal to the gateway list, and this
// literal keeps the form equal to agentBackends.ts, so a drift anywhere fails a test.
const GATEWAY_BACKENDS = ['mock', 'openai', 'anthropic', 'openai-compatible', 'self-hosted'];
const identity: any = { subject: 'owner', roles: ['admin'], permissions: { agents_write: true }, services: ['agents'] };
const projects = [{ id: 'p1', name: 'Project one' }];

function api() {
  const calls: Array<{ url: string; method: string; body: any }> = [];
  globalThis.fetch = async (input, init) => {
    calls.push({ url: String(input), method: init?.method || 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return { ok: true, status: 200, text: async () => JSON.stringify({ items: [] }) } as Response;
  };
  return calls;
}

function renderForm(onDeployed = () => {}) {
  return render(<DeployAgentForm identity={identity} project="p1" projects={projects} tools={[]} existing={[]} onDeployed={onDeployed} />);
}

test('backend list equals the gateway values with labels equal to values', () => {
  assert.deepEqual(AGENT_LLM_BACKENDS.map(item => item.value), GATEWAY_BACKENDS);
  for (const item of AGENT_LLM_BACKENDS) assert.equal(item.label, item.value);
  assert.equal(DEFAULT_AGENT_LLM_BACKEND, 'mock');
});

test('deploy form renders exactly the gateway backends in order and defaults to mock', () => {
  api();
  const view = renderForm();
  const select = view.getByLabelText('Model backend') as HTMLSelectElement;
  assert.deepEqual(Array.from(select.options).map(option => option.value), GATEWAY_BACKENDS);
  assert.deepEqual(Array.from(select.options).map(option => option.textContent), GATEWAY_BACKENDS);
  assert.equal(select.value, 'mock');
});

test('regression: vllm is not offered and the retired option list is gone', () => {
  api();
  const view = renderForm();
  const values = Array.from((view.getByLabelText('Model backend') as HTMLSelectElement).options).map(option => option.value);
  assert.equal(values.includes('vllm'), false);
  assert.notDeepEqual(values, ['mock', 'openai', 'anthropic', 'vllm']);
  for (const wanted of ['openai-compatible', 'self-hosted']) assert.ok(values.includes(wanted), `${wanted} must be selectable`);
});

for (const backend of GATEWAY_BACKENDS) {
  test(`choosing ${backend} posts llm_backend ${backend} unchanged`, async () => {
    const calls = api();
    let deployed = false;
    const view = renderForm(() => { deployed = true; });
    fireEvent.change(view.getByLabelText('Name'), { target: { value: 'support' } });
    fireEvent.change(view.getByLabelText('Image'), { target: { value: 'registry/support:1' } });
    const select = view.getByLabelText('Model backend') as HTMLSelectElement;
    fireEvent.change(select, { target: { value: backend } });
    assert.equal(select.value, backend);
    fireEvent.click(view.getByRole('button', { name: 'Deploy agent' }));
    await waitFor(() => assert.equal(deployed, true));
    const post = calls.find(call => call.method === 'POST' && call.url === '/api/v1/agents');
    assert.ok(post, 'the form posts to /api/v1/agents');
    assert.equal(post.body.llm_backend, backend);
  });
}
