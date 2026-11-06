import {test} from 'node:test';
import assert from 'node:assert/strict';
import {clampToVisible} from '../components/ui/graphBounds';

const size={width:180,height:100};
const visible={x:0,y:0,width:800,height:600};

test('a dragged node stays inside the visible area on every side', () => {
  assert.deepEqual(clampToVisible({x:5000,y:5000},size,visible),{x:620,y:500});
  assert.deepEqual(clampToVisible({x:-300,y:-300},size,{...visible,x:-100,y:-100}),{x:0,y:0});
  assert.deepEqual(clampToVisible({x:100,y:100},size,{...visible,x:200,y:300}),{x:200,y:300});
});

test('a position already in view is unchanged', () => {
  assert.deepEqual(clampToVisible({x:40,y:60},size,visible),{x:40,y:60});
});

test('a node larger than the area stays at the start edge', () => {
  assert.deepEqual(clampToVisible({x:90,y:90},{width:900,height:900},visible),{x:0,y:0});
});
