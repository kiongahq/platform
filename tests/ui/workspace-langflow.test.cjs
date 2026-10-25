const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require('jsdom');

test('Langflow workspace launches the selected instance through Kionga', async t => {
  const id = 'langflow-1234567890abcdef12345678';
  const dom = new JSDOM(fs.readFileSync('go/cmd/gateway/web/workspace.html', 'utf8'), {
    url: `http://localhost:8080/workspace.html?tool=langflow&instance=${id}&project=p1`,
    runScripts: 'outside-only',
  });
  t.after(() => dom.window.close());
  const requests = [];
  dom.window.fetch = async (path, options) => {
    requests.push({path, options});
    const data = path.endsWith('/project-options') ? {items:[{id:'p1',name:'Project one'}]} :
      path.endsWith('/workspaces') ? {items:[{id:'workbench',available:false},{id:'ide',available:false},{id:'langflow',available:false}]} :
      {url:'/workspaces/langflow/'};
    return {ok:true,status:200,headers:{get:()=> 'application/json'},json:async()=>data};
  };
  dom.window.eval(fs.readFileSync('go/cmd/gateway/web/workspace.js','utf8'));
  await new Promise(resolve => setTimeout(resolve, 25));
  assert.equal(requests[2].path, `/api/v1/langflow-workspaces/${id}/launch`);
  assert.deepEqual(JSON.parse(requests[2].options.body), {project_id:'p1'});
  assert.equal(dom.window.document.querySelector('#workspace-frame').getAttribute('src'), '/workspaces/langflow/');
  assert.match(dom.window.document.querySelector('#workspace-status').textContent,/Langflow/);
});
