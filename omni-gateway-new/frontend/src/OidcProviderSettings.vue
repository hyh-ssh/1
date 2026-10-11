<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type OidcProviderInput, type OidcProviderView } from './api'

// 主 OIDC Provider 的管理表单（控制台「设置」）。配置与「是否已设置密钥」都来自
// /api/admin/oidc-provider：client_secret 永不回显，留空表示保留原值，显式清空走 DELETE
// （后端语义是置空 + enabled=false + 保留记录，不是删行）。
const loading = ref(true)
const loadError = ref('')
const status = ref<OidcProviderView | null>(null)
const saving = ref(false)
const clearing = ref(false)
const error = ref('')
const notice = ref('')

const form = ref<OidcProviderInput>({
  enabled: false,
  issuer: '',
  client_id: '',
  client_secret: '',
  redirect_url: '',
  display_name: '',
})

const secretStored = computed(() => status.value?.has_client_secret === true)
const secretLabel = computed(() => (secretStored.value ? '已设置' : '未设置'))
// 只要任一配置项有值或已存密钥，就算「已配置」；enabled 只表示 SSO 登录是否启用。
const configured = computed(() => {
  const current = status.value
  if (!current) return false
  return current.ready === true || !!(current.issuer || current.client_id || current.redirect_url || current.display_name || current.has_client_secret)
})
const busy = computed(() => saving.value || clearing.value)

// 服务端视图是唯一事实来源：每次保存/清空后都用响应重新填表，而不是本地拼接。
function applyStatus(view: OidcProviderView | null) {
  status.value = view
  form.value = {
    enabled: view?.enabled === true,
    issuer: view?.issuer || '',
    client_id: view?.client_id || '',
    // 密钥不回显：输入框从任何接口都拿不到明文，保存时留空即保留原值。
    client_secret: '',
    redirect_url: view?.redirect_url || '',
    display_name: view?.display_name || '',
  }
}

async function load() {
  loading.value = true
  loadError.value = ''
  error.value = ''
  notice.value = ''
  const result = await api.oidcProvider.get()
  if (result.error) loadError.value = result.error
  else applyStatus(result.data || null)
  loading.value = false
}

onMounted(load)

async function save() {
  if (busy.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    // client_secret 留空时该字段整个缺席（见 api.oidcProviderPayload）：空值不会发出去，
    // 所以这里不需要、也不能在本地把已存密钥抹掉。
    const result = await api.oidcProvider.save({ ...form.value })
    if (result.error) throw new Error(result.error)
    applyStatus(result.data || null)
    notice.value = '主 OIDC Provider 配置已保存。'
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : '保存失败'
  } finally {
    saving.value = false
  }
}

async function clearProvider() {
  if (busy.value) return
  if (!window.confirm('确定清空主 OIDC Provider 配置？清空后 OAuth 登录立即停用，需要重新配置才能恢复。')) return
  clearing.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await api.oidcProvider.clear()
    if (result.error) throw new Error(result.error)
    // 清空后重新读取服务端状态：它返回的是置空后的视图，本地猜不出 updated_at 之类的字段。
    const refreshed = await api.oidcProvider.get()
    if (refreshed.error) error.value = refreshed.error
    else applyStatus(refreshed.data || null)
    if (!error.value) notice.value = '配置已清空，OAuth 登录已停用。'
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : '清空失败'
  } finally {
    clearing.value = false
  }
}
</script>

<template>
  <div class="toolbar">
    <div>
      <h2>设置</h2>
      <p class="muted">主 OIDC Provider 是控制台唯一的单点登录入口；客户端密钥只写不读，保存时留空表示保留原值。</p>
    </div>
    <div class="toolbar-actions"><button class="secondary" :disabled="busy || loading" @click="load">↻ 刷新</button></div>
  </div>
  <p v-if="loading" class="muted" role="status">加载中…</p>
  <div v-else-if="loadError" class="alert">{{ loadError }} <button @click="load">重试</button></div>
  <template v-else>
    <div v-if="error" class="alert" role="alert">{{ error }}</div>
    <div v-if="notice" class="notice" role="status">{{ notice }}</div>

    <article class="panel table-panel">
      <h3 class="panel-title">当前状态</h3>
      <div class="table readonly">
        <div class="table-head"><span>项目</span><span>状态</span><span>当前值</span></div>
        <div class="table-row"><strong>配置状态</strong><span class="tag" :class="{ inactive: !configured }">{{ configured ? '已配置' : '未配置' }}</span><span class="muted">{{ configured ? '已保存到网关的 OIDC Provider 配置' : '尚未配置任何 OIDC Provider' }}</span></div>
        <div class="table-row"><strong>OAuth 登录</strong><span class="tag" :class="{ inactive: !status?.enabled }">{{ status?.enabled ? '已启用' : '未启用' }}</span><span class="muted">{{ status?.ready ? '配置完整，可用于登录' : '启用且四项必填齐全后才可用于登录' }}</span></div>
        <div class="table-row"><strong>Issuer</strong><span class="muted">—</span><span class="muted">{{ status?.issuer || '—' }}</span></div>
        <div class="table-row"><strong>Client ID</strong><span class="muted">—</span><span class="muted">{{ status?.client_id || '—' }}</span></div>
        <div class="table-row"><strong>Client Secret</strong><span class="muted">—</span><span class="muted">{{ secretLabel }}</span></div>
        <div class="table-row"><strong>回调地址</strong><span class="muted">—</span><span class="muted">{{ status?.redirect_url || '—' }}</span></div>
        <div class="table-row"><strong>显示名称</strong><span class="muted">—</span><span class="muted">{{ status?.display_name || '—' }}</span></div>
      </div>
    </article>

    <article class="panel form-panel">
      <h3>主 OIDC Provider</h3>
      <div class="form-grid">
        <label class="check wide"><input v-model="form.enabled" type="checkbox" :disabled="busy" />启用 OAuth 登录</label>
        <label class="wide">Issuer<input v-model="form.issuer" :disabled="busy" placeholder="https://idp.example.com" /></label>
        <label>Client ID<input v-model="form.client_id" :disabled="busy" /></label>
        <label>回调地址<input v-model="form.redirect_url" :disabled="busy" placeholder="https://网关域名/api/auth/oidc/callback" /></label>
        <label>Client Secret<input v-model="form.client_secret" type="password" autocomplete="new-password" :disabled="busy" :placeholder="secretStored ? '已设置，留空表示不修改' : '未设置，请填写'" /></label>
        <label>显示名称<input v-model="form.display_name" :disabled="busy" placeholder="例如：企业统一登录" /></label>
      </div>
      <p class="muted">Client Secret 永不回显：留空保存表示保留已设置的值；要清除密钥请使用「清空配置」，那会同时停用 OAuth 登录。</p>
      <div class="toolbar-actions">
        <button class="primary compact" :disabled="busy" @click="save">{{ saving ? '保存中…' : '保存' }}</button>
        <button class="secondary danger" :disabled="busy" @click="clearProvider">{{ clearing ? '清空中…' : '清空配置' }}</button>
      </div>
    </article>
  </template>
</template>
