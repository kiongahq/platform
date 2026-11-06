import './setup-dom';
import {test, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import React, {useState} from 'react';
import {render, fireEvent, cleanup} from '@testing-library/react';
import {Select} from '../components/ui/Select';
afterEach(cleanup);

function Field({onChange}: {onChange?: (value: string) => void}) {
  const [value, setValue] = useState('a');
  return <label>Kind<Select value={value} onChange={(e) => {setValue(e.target.value); onChange?.(e.target.value);}}>
    <option value="a">Alpha</option><option value="b" disabled>Beta</option><option value="c">Gamma</option>
  </Select></label>;
}

test('clicking opens the application list rather than the browser one', () => {
  const view = render(<Field />);
  const select = view.getByLabelText('Kind');
  assert.equal(fireEvent.mouseDown(select), false, 'the browser list must be prevented');
  assert.equal(view.getAllByRole('option', {hidden: true}).filter((o) => o.tagName === 'LI').length, 3);
});

test('choosing an item updates the value and reports the change', () => {
  const seen: string[] = [];
  const view = render(<Field onChange={(v) => seen.push(v)} />);
  fireEvent.mouseDown(view.getByLabelText('Kind'));
  fireEvent.click(view.container.querySelectorAll('.select-list li')[2]);
  assert.deepEqual(seen, ['c']);
  assert.equal((view.getByLabelText('Kind') as HTMLSelectElement).value, 'c');
  assert.equal(view.container.querySelector('.select-list'), null);
});

test('a disabled item can not be chosen, and the keyboard skips it', () => {
  const seen: string[] = [];
  const view = render(<Field onChange={(v) => seen.push(v)} />);
  const select = view.getByLabelText('Kind');
  fireEvent.mouseDown(select);
  fireEvent.click(view.container.querySelectorAll('.select-list li')[1]);
  assert.deepEqual(seen, []);
  fireEvent.keyDown(select, {key: 'ArrowDown'});
  fireEvent.keyDown(select, {key: 'Enter'});
  assert.deepEqual(seen, ['c']);
});

test('Escape closes without changing the value', () => {
  const seen: string[] = [];
  const view = render(<Field onChange={(v) => seen.push(v)} />);
  const select = view.getByLabelText('Kind');
  fireEvent.keyDown(select, {key: 'ArrowDown'});
  assert.ok(view.container.querySelector('.select-list'));
  fireEvent.keyDown(select, {key: 'Escape'});
  assert.equal(view.container.querySelector('.select-list'), null);
  assert.deepEqual(seen, []);
});

test('typing a letter moves to the matching item', () => {
  const seen: string[] = [];
  const view = render(<Field onChange={(v) => seen.push(v)} />);
  const select = view.getByLabelText('Kind');
  fireEvent.keyDown(select, {key: 'ArrowDown'});
  fireEvent.keyDown(select, {key: 'g'});
  fireEvent.keyDown(select, {key: 'Enter'});
  assert.deepEqual(seen, ['c']);
});

test('disabled and multiple selects open no list', () => {
  const view = render(<><Select aria-label="off" disabled><option>x</option></Select><Select aria-label="many" multiple><option>x</option></Select></>);
  fireEvent.mouseDown(view.getByLabelText('off'));
  fireEvent.mouseDown(view.getByLabelText('many'));
  assert.equal(view.container.querySelector('.select-list'), null);
});
