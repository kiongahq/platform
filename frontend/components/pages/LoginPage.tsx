"use client";
import { useEffect, useState } from "react";

interface Provider { id: string; name: string; type?: string }
interface SignInOptions { local?: boolean; providers?: Provider[] }
function safeReturn(value: string | null) {
  if (!value || !value.startsWith('/') || value.startsWith('//') || /[\\\u0000-\u0020]/.test(value)) return '/console';
  try { const decoded = decodeURIComponent(value); if (decoded.startsWith('//') || /[\\\u0000-\u0020]/.test(decoded)) return '/console'; const parsed = new URL(value, window.location.origin); return parsed.origin === window.location.origin ? parsed.pathname + parsed.search : '/console'; } catch { return '/console'; }
}
export function LoginPage() {
  const [returnTo, setReturnTo] = useState('/console');
  const [error, setError] = useState('');
  const [options, setOptions] = useState<SignInOptions>({ local: true, providers: [] });
  useEffect(() => {
    const query = new URLSearchParams(window.location.search);
    setReturnTo(safeReturn(query.get('return_to')));
    setError(({ invalid: 'Those credentials were not accepted.', throttled: 'Too many failed attempts for this account. Wait a minute and try again.' } as Record<string,string>)[query.get('error') || ''] || '');
    const controller = new AbortController();
    fetch('/auth/providers', { signal: controller.signal, headers: { Accept: 'application/json' } })
      .then(response => response.ok ? response.json() : undefined).then(value => { if (value) setOptions(value); }).catch(() => {});
    return () => controller.abort();
  }, []);
  const providers = Array.isArray(options.providers) ? options.providers : [];
  const local = options.local !== false;
  return <div className="login-page">
    <a className="wordmark" href="/"><img src="/logo.svg" alt="" width="87" height="24" /><strong>Kionga</strong></a>
    <main className="login-shell">
      <section className="login-copy"><h1>Your work, connected.</h1><p>Projects, pipelines, models, agents and persistent workspaces on infrastructure you control.</p><p className="muted">Access is governed by your organization’s platform policies.</p><a href="/api-docs">Read the API reference</a></section>
      <section className="login-card"><h2>Sign in to Kionga</h2><p id="login-hint">{providers.length ? (local ? 'Use your organization account, or a local account.' : 'Use your organization account.') : 'Use your local platform credentials.'}</p>
        <div className="sso-options" id="sso-options" hidden={!providers.length} aria-label="Single sign-on">{providers.map(provider => <a key={provider.id} className="button" data-provider={provider.id} data-type={provider.type} href={`/auth/sso/login?${new URLSearchParams({ connector: provider.id, return_to: returnTo })}`}>Sign in with {provider.name}</a>)}</div>
        <p className="login-divider" id="login-divider" hidden={!(local && providers.length)}>Or use a local account</p>
        <form method="post" action="/auth/local/login" id="local-login" hidden={!local}>
          <input type="hidden" name="return_to" id="return-to" value={returnTo} />
          <label>Username<input name="username" autoComplete="username" required autoFocus /></label>
          <label>Password<input name="password" type="password" autoComplete="current-password" required /></label>
          <p className="feedback error" id="login-error" role="alert" hidden={!error}>{error}</p>
          <button className="primary" type="submit">Sign in</button>
        </form>
        <p className="muted" id="login-note" hidden={providers.length > 0}>Your organization may provide additional single sign-on options.</p>
      </section>
    </main>
  </div>;
}
