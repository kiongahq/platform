import './setup-dom';
import {test,afterEach} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {render,fireEvent,waitFor,cleanup,act} from '@testing-library/react';
import {SessionNotice} from '../components/layout/SessionNotice';
import {request,APIError} from '../lib/api';
afterEach(cleanup);
test('expired API requests preserve the current editor and reconnect after signing in',async()=>{
 let authenticated=false;globalThis.fetch=async()=>new Response(JSON.stringify(authenticated?{subject:'admin'}:{message:'Sign in to continue.'}),{status:authenticated?200:401});
 const view=render(<><input aria-label="Draft title" defaultValue="Unsaved article"/><SessionNotice/></>);
 await assert.rejects(request('/api/v1/me'),(error:any)=>error instanceof APIError&&error.status===401);
 await waitFor(()=>assert.ok(view.getByRole('alert')));
 assert.ok(view.getByRole('alert'));assert.equal((view.getByRole('link',{name:'Sign in'}) as HTMLAnchorElement).target,'_blank');
 assert.equal((view.getByLabelText('Draft title') as HTMLInputElement).value,'Unsaved article');
 authenticated=true;fireEvent.click(view.getByRole('button',{name:'Reconnect'}));await waitFor(()=>assert.ok(view.queryByRole('alert')===null));
 assert.equal((view.getByLabelText('Draft title') as HTMLInputElement).value,'Unsaved article');
});
