import { useAuthStore } from '@/features/auth/store';

// OAuth uses browser redirects and the legacy Echo boundary rather than generated JSON RPC.
export interface OAuthConfig {
  authorizationUrl: string;
  tokenUrl: string;
  userinfoUrl: string;
  clientId: string;
  clientSecret: string;
  redirectUrl: string;
  scope: string;
  subject?: string;
}
export async function oauthRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(`/sd-api/oauth/${path}`, {
    method,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', Token: useAuthStore().currentAccessToken() },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (!response.ok) {
    const error = (await response.json().catch(() => null)) as { message?: string } | null;
    throw new Error(error?.message || 'OAuth 请求失败');
  }
  return response.status === 204 ? (undefined as T) : ((await response.json()) as T);
}
export async function startOAuth(mode: 'login' | 'bind'): Promise<void> {
  const result = await oauthRequest<{ url: string }>(mode, 'POST', {});
  window.location.assign(result.url);
}
