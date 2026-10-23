// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from 'vitest';
import { shallowMount, flushPromises } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { useAuthStore } from '@/features/auth/store';
import OAuthSettings from './OAuthSettings.vue';
import { NButton } from 'naive-ui';

vi.mock('naive-ui', async original => ({
  ...(await original<typeof import('naive-ui')>()),
  useMessage: () => ({ success: vi.fn(), error: vi.fn() }),
}));
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it('saves source configuration before explicitly starting a browser binding', async () => {
  setActivePinia(createPinia());
  useAuthStore().setAccessToken('admin');
  const config = {
    authorizationUrl: 'https://provider.example/authorize',
    tokenUrl: 'https://provider.example/token',
    userinfoUrl: 'https://provider.example/userinfo',
    clientId: 'client',
    clientSecret: '',
    redirectUrl: 'https://dice.example/sd-api/oauth/callback',
    scope: 'openid',
    subject: '',
  };
  const fetch = vi.fn(async (path: string, options: RequestInit) => {
    if (path.endsWith('/bind'))
      return new Response(JSON.stringify({ url: 'https://provider.example/authorize?state=once' }));
    if (options.method === 'PUT') return new Response(null, { status: 204 });
    return new Response(JSON.stringify(config));
  });
  vi.stubGlobal('fetch', fetch);
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  const wrapper = shallowMount(OAuthSettings, { global: { renderStubDefaultSlot: true } });
  await flushPromises();
  const buttons = wrapper.findAllComponents(NButton);
  const save = buttons.find(button => button.text() === '保存 OAuth 源');
  expect(save).toBeDefined();
  save!.vm.$emit('click');
  await flushPromises();
  expect(fetch).toHaveBeenCalledWith(
    '/sd-api/oauth/config',
    expect.objectContaining({ method: 'PUT', body: JSON.stringify(config) })
  );
  expect(assign).not.toHaveBeenCalled();
  const bind = buttons.find(button => button.text().includes('绑定已保存'));
  bind!.vm.$emit('click');
  await flushPromises();
  expect(fetch).toHaveBeenCalledWith(
    '/sd-api/oauth/bind',
    expect.objectContaining({ method: 'POST' })
  );
  expect(assign).toHaveBeenCalledWith('https://provider.example/authorize?state=once');
  wrapper.unmount();
});
