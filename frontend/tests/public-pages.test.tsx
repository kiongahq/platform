import './setup-dom';
import React from 'react';
import { test, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import { render, cleanup, waitFor, fireEvent } from '@testing-library/react';
import { LoginPage } from '../components/pages/LoginPage';
import { WorkspacePage } from '../components/pages/WorkspacePage';
import { ApiDocsPage } from '../components/pages/ApiDocsPage';
import { RootRedirect } from '../components/pages/RootRedirect';
import { safeWebsiteURL, websiteHref } from '../lib/site';
import { publicationHref } from '../lib/publication';
Object.assign(globalThis, { self: window });
function setup(path: string, handler: (path: string, options?: RequestInit) => unknown) {
  history.replaceState(null, '', path);
  globalThis.fetch = async (path, options) => ({ ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => handler(String(path), options) }) as unknown as Response;
}
afterEach(cleanup);
test('login displays organization providers safely and retains local fallback', async () => {
  setup('/login?return_to=%2Fconsole%2Fprojects', () => ({local:true,providers:[{id:'team',name:'<img src=x onerror=bad()>',type:'oidc'}]}));
  const view=render(<LoginPage/>);
  await waitFor(()=>assert.equal(view.container.querySelectorAll('#sso-options a').length,1));
  const link=view.container.querySelector('#sso-options a')!;
  assert.equal(link.textContent,'Sign in with <img src=x onerror=bad()>');assert.equal(link.querySelector('img'),null);
  assert.equal((view.container.querySelector('#return-to') as HTMLInputElement).value,'/console/projects');
  assert.equal((view.container.querySelector('#local-login') as HTMLElement).hidden,false);
});
test('login rejects encoded off-site return paths and respects SSO-only configuration', async () => {
  setup('/login?return_to=%2F%255Cevil.test',()=>({local:false,providers:[{id:'sso',name:'Organization'}]}));
  const view=render(<LoginPage/>);
  await waitFor(()=>assert.equal((view.container.querySelector('#local-login') as HTMLElement).hidden,true));
  assert.equal((view.container.querySelector('#return-to') as HTMLInputElement).value,'/console');
});
test('workspace launches the exact Langflow instance with its project', async () => {
  const calls: {path:string;options?:RequestInit}[]=[];
  setup('/workspace?tool=langflow&instance=langflow-test&project=p1',(path,options)=>{calls.push({path,options});return path.endsWith('/project-options')?{items:[{id:'p1',name:'Fraud detection'}]}:path.endsWith('/workspaces')?{items:[{id:'workbench',available:true},{id:'langflow',available:false}]}:{url:'/workspaces/langflow/'};});
  const view=render(<WorkspacePage/>);
  await waitFor(()=>assert.equal(view.container.querySelector('#workspace-frame')?.getAttribute('src'),'/workspaces/langflow/'));
  const launch=calls.find(call=>call.path.endsWith('/launch'))!;
  assert.equal(launch.path,'/api/v1/langflow-workspaces/langflow-test/launch');
  assert.deepEqual(JSON.parse(String(launch.options?.body)),{project_id:'p1'});
  assert.equal((view.container.querySelector('#workspace-tool') as HTMLSelectElement).value,'langflow');
  assert.equal(view.container.querySelector('#workspace-project')?.textContent,'Fraud detection');
});
test('an unavailable selected workspace is not replaced by another available tool', async () => {
  const calls:string[]=[];
  setup('/workspace?tool=ide',(path)=>{calls.push(path);return path.endsWith('/project-options')?{items:[]}:{items:[{id:'ide',available:false,message:'IDE is offline'},{id:'workbench',available:true}]};});
  const view=render(<WorkspacePage/>);
  await waitFor(()=>assert.match(view.container.textContent||'',/IDE is offline/));
  assert.equal(calls.some(path=>path.endsWith('/launch')),false);
  assert.equal((view.container.querySelector('#workspace-tool') as HTMLSelectElement).value,'ide');
});
test('API reference uses a dedicated Swagger documentation surface', () => {
  const view=render(<ApiDocsPage/>);
  assert.match(view.getByRole('heading', {level: 1}).textContent||'', /Kionga API reference/);
  assert.equal(view.getByRole('link',{name:'OpenAPI JSON'}).getAttribute('href'), '/api/openapi.json');
  assert.equal(view.container.querySelector('.site-header'), null);
});
test('the install has no landing page: the root sends people to the console', () => {
  const view=render(<RootRedirect/>);
  assert.equal(view.getByRole('link',{name:'Continue'}).getAttribute('href'),'/console');
  assert.equal(view.container.querySelector('a[href="/blog"]'), null);
});
test('login shows the website link only when the install configures one', async () => {
  setup('/login', path => path === '/api/v1/site' ? {website_url:'https://kionga.dev/'} : {local:true,providers:[]});
  const view=render(<LoginPage/>);
  await waitFor(()=>assert.equal(view.container.querySelector('#website-link')?.getAttribute('href'),'https://kionga.dev/'));
  cleanup();
  setup('/login', path => path === '/api/v1/site' ? {website_url:''} : {local:true,providers:[]});
  const hidden=render(<LoginPage/>);
  await waitFor(()=>assert.equal((hidden.container.querySelector('#local-login') as HTMLElement).hidden,false));
  assert.equal(hidden.container.querySelector('#website-link'), null);
});
test('website and publication links accept only absolute http(s) URLs', () => {
  assert.equal(safeWebsiteURL('https://kionga.dev'), 'https://kionga.dev/');
  for (const value of ['javascript:alert(1)', 'https://user:pw@kionga.dev', '/relative', '', 42, undefined]) assert.equal(safeWebsiteURL(value), '', String(value));
  assert.equal(websiteHref('', '/blog'), '');
  assert.equal(websiteHref('https://kionga.dev/', '/blog'), 'https://kionga.dev/blog');
  assert.equal(publicationHref('/blog'), '');
  assert.equal(publicationHref('/blog/read?slug=a', 'https://kionga.dev/'), 'https://kionga.dev/blog/read?slug=a');
});
