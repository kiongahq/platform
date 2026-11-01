"use client";
import {useEffect, useState} from 'react';
import {request} from '@/lib/api';
export function SessionNotice() {
  const [expired,setExpired]=useState(false),[error,setError]=useState('');
  useEffect(()=>{const expire=()=>setExpired(true);window.addEventListener('kionga:session-expired',expire);return()=>window.removeEventListener('kionga:session-expired',expire);},[]);
  if(!expired)return null;
  return <aside className="feedback error" role="alert"><p>Your session has expired. Sign in in another tab to keep unsaved work open here.</p><div className="button-row"><a className="button" href={`/login?return_to=${encodeURIComponent(location.pathname+location.search)}`} target="_blank" rel="noopener">Sign in</a><button onClick={async()=>{try{await request('/api/v1/me');setExpired(false);setError('');window.dispatchEvent(new window.Event('kionga:refresh'));}catch{setError('Sign in before reconnecting.');}}}>Reconnect</button></div>{error&&<p>{error}</p>}</aside>;
}
