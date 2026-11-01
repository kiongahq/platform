import './setup-dom';
import React from 'react';
import {test, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {render, fireEvent, cleanup, within} from '@testing-library/react';
import {SampleWorkspaces} from '../components/landing/SampleWorkspaces';
import {evaluateSample, SAMPLE_CSV, inspectProject, PROJECT_CONFIG, projectFiles, renderSamplePrompt, routeSample} from '../lib/landing-samples';
Object.assign(globalThis, {self: window});
afterEach(cleanup);

test('classifier sample computes its confusion matrix from edited data and rejects malformed rows', () => {
  const result = evaluateSample(SAMPLE_CSV, .5);
  assert.equal(result.accuracy, .75);
  assert.deepEqual(result.matrix, {truePositive:3, falsePositive:1, trueNegative:3, falseNegative:1});
  assert.equal(evaluateSample(SAMPLE_CSV, .9).accuracy, .625);
  for (const csv of ['actual,probability\n1,', 'actual,probability\n0,NaN', 'actual,probability\n2,0.2', 'actual,probability\n1,1.1']) assert.throws(() => evaluateSample(csv, .5), /Row 2/);
  assert.throws(() => evaluateSample(SAMPLE_CSV, Infinity), /threshold/);
});
test('project checker rejects missing dependencies and cycles and generates files from validated settings', () => {
  const config = JSON.parse(PROJECT_CONFIG); config.estimator = 'random_forest';
  const project = inspectProject(JSON.stringify(config));
  assert.deepEqual(project.order, ['prepare', 'train', 'evaluate']);
  assert.match(projectFiles(project)['train.py'], /from sklearn.ensemble import RandomForestClassifier/);
  config.jobs[0].depends_on = ['evaluate'];
  assert.throws(() => inspectProject(JSON.stringify(config)), /cycle/);
  config.jobs[0].depends_on = ['missing'];
  assert.throws(() => inspectProject(JSON.stringify(config)), /dependency/);
});
test('prompt rendering substitutes literal input and routing trace follows the matching branch', () => {
  assert.equal(renderSamplePrompt('Inspect {input} then {input}', '$& <script>'), 'Inspect $& <script> then $& <script>');
  assert.throws(() => renderSamplePrompt('No variable', 'text'), /\{input\}/);
  assert.deepEqual(routeSample('REFUND order 42', 'refund').path, ['input', 'condition', 'review', 'output']);
  assert.deepEqual(routeSample('Opening hours?', 'refund').path, ['input', 'condition', 'answer', 'output']);
});
test('sample workspace interactions retain edits across keyboard tab changes and invalidate stale results', () => {
  const view = render(<SampleWorkspaces/>);
  const notebook = view.getByRole('tabpanel');
  fireEvent.click(within(notebook).getByRole('button', {name:'Run cell'}));
  assert.ok(within(notebook).getByText('75.0%'));
  fireEvent.change(within(notebook).getByRole('slider'), {target:{value:'.9'}});
  assert.equal(within(notebook).queryByText('75.0%'), null);
  fireEvent.keyDown(view.getByRole('tab', {name:'JupyterLab'}), {key:'ArrowRight'});
  const ide = view.getByRole('tabpanel');
  assert.equal(document.activeElement?.textContent, 'Browser IDE');
  fireEvent.change(within(ide).getByLabelText('Sample project configuration'), {target:{value:'{broken'}});
  fireEvent.click(within(ide).getByRole('button', {name:'Check project'}));
  assert.match(within(ide).getByRole('alert').textContent || '', /invalid JSON/);
  fireEvent.click(view.getByRole('tab', {name:'JupyterLab'}));
  assert.equal((within(view.getByRole('tabpanel')).getByRole('slider') as HTMLInputElement).value, '0.9');
  fireEvent.click(view.getByRole('tab', {name:'Browser IDE'}));
  assert.equal((view.getByLabelText('Sample project configuration') as HTMLTextAreaElement).value, '{broken');
});
test('flow sample displays escaped rendered input and native sample produces a real local decision', () => {
  const view = render(<SampleWorkspaces/>);
  fireEvent.click(view.getByRole('tab', {name:'Langflow'}));
  const flow = view.getByRole('tabpanel');
  fireEvent.change(within(flow).getByLabelText('Incident text'), {target:{value:'<img src=x onerror=bad()>'}});
  fireEvent.click(within(flow).getByRole('button', {name:'Render prompt'}));
  assert.match(flow.querySelector('.sample-code-view .numbered-code')?.textContent || '', /<img src=x/);
  assert.equal(flow.querySelector('img'), null);
  fireEvent.click(view.getByRole('tab', {name:'Agent designer'}));
  const agent = view.getByRole('tabpanel');
  fireEvent.click(within(agent).getByRole('button', {name:'Run routing sample'}));
  assert.match(within(agent).getByRole('status').textContent || '', /human review/);
  fireEvent.click(within(agent).getByRole('button', {name:'Try another request'}));
  assert.equal(within(agent).queryByRole('status'), null);
  fireEvent.click(within(agent).getByRole('button', {name:'Run routing sample'}));
  assert.match(within(agent).getByRole('status').textContent || '', /answer workflow/);
});
