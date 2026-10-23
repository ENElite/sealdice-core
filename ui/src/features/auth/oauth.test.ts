import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { useAuthStore } from './store';
import { oauthRequest, startOAuth } from './oauth';

beforeEach(() => {
  setActivePinia(createPinia());
});
afterEach(() => {
  vi.unstubAllGlobals();
});

it('uses the existing administrator token for binding configuration', async () => {
  useAuthStore().setAccessToken('admin-token');
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ subject: 'bound-id' })));
  vi.stubGlobal('fetch', fetch);
  expect(await oauthRequest('config')).toEqual({ subject: 'bound-id' });
  expect(fetch).toHaveBeenCalledWith(
    '/sd-api/oauth/config',
    expect.objectContaining({
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', Token: 'admin-token' },
    })
  );
});

it('surfaces provider configuration errors without accepting a failed login', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: '未绑定' }), { status: 403 }))
  );
  await expect(oauthRequest('login', 'POST', {})).rejects.toThrow('未绑定');
});

it('accepts a successful configuration save without parsing an empty response', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
  await expect(oauthRequest('config', 'PUT', {})).resolves.toBeUndefined();
});

it('starts only the selected OAuth flow and navigates to its authorization URL', async () => {
  const assign = vi.fn();
  vi.stubGlobal('window', { location: { assign } });
  const fetch = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify({ url: 'https://provider.example/authorize?state=once' }))
    );
  vi.stubGlobal('fetch', fetch);
  await startOAuth('bind');
  expect(fetch).toHaveBeenCalledWith(
    '/sd-api/oauth/bind',
    expect.objectContaining({ method: 'POST', body: '{}' })
  );
  expect(assign).toHaveBeenCalledWith('https://provider.example/authorize?state=once');
});
