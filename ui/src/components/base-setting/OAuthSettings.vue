<template>
  <n-collapse class="oauth-settings">
    <n-collapse-item title="OAuth 登录" name="oauth">
      <n-space vertical>
        <n-text depth="3">先保存 WebUI 密码，再配置标准 OAuth 源。保存源后需重新绑定账号。</n-text>
        <n-form-item v-for="field in fields" :key="field.key" :label="field.label">
          <n-input
            v-model:value="config[field.key]"
            :type="field.key === 'clientSecret' ? 'password' : 'text'"
            :placeholder="field.key === 'clientSecret' ? '留空保持原值' : field.label"
            :disabled="busy"
          />
        </n-form-item>
        <n-text>{{ config.subject ? `已绑定：${config.subject}` : '尚未绑定第三方账号' }}</n-text>
        <n-space>
          <n-button :loading="busy" @click="save">保存 OAuth 源</n-button>
          <n-button :disabled="busy || !config.clientId" @click="bind"
            >绑定已保存的 OAuth 源</n-button
          >
          <n-button :disabled="busy || !config.subject" @click="unbind">解除绑定</n-button>
        </n-space>
      </n-space>
    </n-collapse-item>
  </n-collapse>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { useMessage } from 'naive-ui';
import { oauthRequest, startOAuth, type OAuthConfig } from '@/features/auth/oauth';

const message = useMessage();
const busy = ref(false);
const config = reactive<OAuthConfig>({
  authorizationUrl: '',
  tokenUrl: '',
  userinfoUrl: '',
  clientId: '',
  clientSecret: '',
  redirectUrl: `${window.location.origin}/sd-api/oauth/callback`,
  scope: 'openid profile',
});
const fields: { key: Exclude<keyof OAuthConfig, 'subject'>; label: string }[] = [
  { key: 'authorizationUrl', label: 'Authorization URL' },
  { key: 'tokenUrl', label: 'Token URL' },
  { key: 'userinfoUrl', label: 'Userinfo URL（返回稳定的 sub）' },
  { key: 'clientId', label: 'Client ID' },
  { key: 'clientSecret', label: 'Client secret' },
  { key: 'redirectUrl', label: '回调 URL（须同时登记在第三方）' },
  { key: 'scope', label: 'Scope' },
];
async function reload() {
  const result = await oauthRequest<OAuthConfig>('config');
  Object.assign(config, result);
  if (!config.redirectUrl) config.redirectUrl = `${window.location.origin}/sd-api/oauth/callback`;
}
onMounted(() => {
  void reload().catch(error => message.error(String(error)));
});
async function save() {
  busy.value = true;
  try {
    await oauthRequest('config', 'PUT', config);
    await reload();
    message.success('OAuth 源已保存，请绑定账号');
  } catch (error) {
    message.error(String(error));
  } finally {
    busy.value = false;
  }
}
async function bind() {
  try {
    await startOAuth('bind');
  } catch (error) {
    message.error(String(error));
  }
}
async function unbind() {
  busy.value = true;
  try {
    await oauthRequest('binding', 'DELETE');
    await reload();
    message.success('已解除绑定');
  } catch (error) {
    message.error(String(error));
  } finally {
    busy.value = false;
  }
}
</script>

<style scoped>
.oauth-settings {
  margin-top: 0.75rem;
}
</style>
