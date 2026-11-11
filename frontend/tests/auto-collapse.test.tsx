import './setup-dom';
import {test, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {render, fireEvent, createEvent, cleanup, act} from '@testing-library/react';
import {useAutoCollapse} from '../components/layout/useAutoCollapse';
afterEach(cleanup);

function Panel({enabled = true}: {enabled?: boolean}) {
  const {expanded, handlers, hold} = useAutoCollapse({enabled, openDelay: 30, closeDelay: 60});
  return <aside data-testid="panel" data-expanded={String(expanded)} {...handlers}>
    <a href="#a">A</a><a href="#b">B</a>
    <button onClick={() => hold(true)}>hold</button><button onClick={() => hold(false)}>release</button>
  </aside>;
}

const wait = (ms: number) => act(() => new Promise((resolve) => setTimeout(resolve, ms)));
const state = (view: ReturnType<typeof render>) => view.getByTestId('panel').dataset.expanded;
// The test DOM drops pointerType from event options, so set it on the event itself.
const pointer = (view: ReturnType<typeof render>, type: 'pointerEnter' | 'pointerLeave', pointerType = 'mouse') => {
  const panel = view.getByTestId('panel');
  // React's onPointerEnter and onPointerLeave listen for pointerover and pointerout.
  const event = createEvent[type === 'pointerEnter' ? 'pointerOver' : 'pointerOut'](panel, {relatedTarget: document.body});
  Object.defineProperty(event, 'pointerType', {value: pointerType});
  fireEvent(panel, event);
};

test('the pointer opens it after the open delay and closes it after the close delay', async () => {
  const view = render(<Panel />);
  pointer(view, 'pointerEnter');
  assert.equal(state(view), 'false', 'a passing cursor must not open it at once');
  await wait(50);
  assert.equal(state(view), 'true');
  pointer(view, 'pointerLeave');
  await wait(20);
  assert.equal(state(view), 'true', 'it stays open briefly after the pointer leaves');
  await wait(70);
  assert.equal(state(view), 'false');
});

test('a quick sweep across the edge never opens it', async () => {
  const view = render(<Panel />);
  pointer(view, 'pointerEnter');
  await wait(10);
  pointer(view, 'pointerLeave');
  await wait(100);
  assert.equal(state(view), 'false');
});

test('touch input is ignored', async () => {
  const view = render(<Panel />);
  pointer(view, 'pointerEnter', 'touch');
  pointer(view, 'pointerEnter', 'pen');
  await wait(50);
  assert.equal(state(view), 'true', 'the same event as a pen opens it, so the touch case is a real check');
  pointer(view, 'pointerLeave', 'pen');
  await wait(100);
  pointer(view, 'pointerEnter', 'touch');
  await wait(50);
  assert.equal(state(view), 'false');
});

test('keyboard focus opens it at once and moving focus inside keeps it open', async () => {
  const view = render(<Panel />);
  const [a, b] = view.getAllByRole('link');
  act(() => a.focus());
  assert.equal(state(view), 'true');
  act(() => b.focus());
  await wait(100);
  assert.equal(state(view), 'true');
  act(() => b.blur());
  await wait(100);
  assert.equal(state(view), 'false');
});

test('hold keeps it open after the pointer leaves until released', async () => {
  const view = render(<Panel />);
  pointer(view, 'pointerEnter');
  await wait(50);
  fireEvent.click(view.getByText('hold'));
  pointer(view, 'pointerLeave');
  await wait(100);
  assert.equal(state(view), 'true');
  fireEvent.click(view.getByText('release'));
  await wait(100);
  assert.equal(state(view), 'false');
});

test('disabled, it never opens; enabled with focus already inside, it opens', async () => {
  const view = render(<Panel enabled={false} />);
  act(() => view.getAllByRole('link')[0].focus());
  pointer(view, 'pointerEnter');
  await wait(50);
  assert.equal(state(view), 'false');
  view.rerender(<Panel enabled />);
  assert.equal(state(view), 'true');
});
