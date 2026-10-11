<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { api } from './api'
import PersonalDialog from './PersonalDialog.vue'
import {
  capabilityLabel, diagnosis, inputUTC, keyStatus, knownProtocols, localInput,
  normalizeAccess, numberText, passwordMode, pricingLabel, protocols, requestContext,
  requestQuery, safeError, settlementLabel, snippets, storedErrorCode, storedRequestID, utcInput, utcTime,
  type KeyAccess, type PersonalContext, type Protocol,
} from './personalConsole'

const props = defineProps<{ user: any; view: 'account' | 'myKeys' | 'myRequests'; context?: Record<string, string> }>()
const emit = defineEmits<{
  'user-refresh': []
  'context-change': [context: Record<string, string>]
  navigate: [target: { view: 'myKeys' | 'myRequests'; context?: Record<string, string> }]
}>()

// The lifetime and per-read generations protect EVERY asynchronous result,
// including a late creation response containing the one-time plaintext.
let lifetime = 0
let alive = true
let summaryRead = 0
let keysRead = 0
let requestsRead = 0
let accessRead = 0
let formAccessRead = 0
let dialogGeneration = 0
let copyGeneration = 0
const current = (generation: number) => alive && generation === lifetime
const paneRoot = ref<HTMLElement | null>(null)
const summary = ref<any>(null)
const summaryLoading = ref(false)
const summaryError = ref('')
const keys = ref<any[]>([])
const keysLoading = ref(false)
const keysError = ref('')
const records = ref<any[]>([])
const recordsLoading = ref(false)
const recordsError = ref('')
const actionSuccess = ref('')
const copyStatus = ref('')
const selectedKeyID = ref('')
const access = ref<KeyAccess | null>(null)
const accessLoading = ref(false)
const accessError = ref('')
const selectedAlias = ref('')
const selectedProtocol = ref<Protocol | ''>('')
const baseURL = `${window.location.origin}/v1`
const localTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || '本机时区'

const selectedKey = computed(() => keys.value.find((key) => String(key.id) === selectedKeyID.value))
const selectedModel = computed(() => access.value?.models.find((model) => model.id === selectedAlias.value))
const availableProtocols = computed(() => knownProtocols(selectedModel.value))
const accessUsable = computed(() => keyStatus(selectedKey.value) === '有效' && access.value?.key_status === 'active')
const example = computed(() => accessUsable.value && selectedModel.value?.ready && selectedProtocol.value && availableProtocols.value.includes(selectedProtocol.value)
  ? snippets(baseURL, selectedAlias.value, selectedProtocol.value) : null)
const mode = computed(() => passwordMode(props.user, summary.value?.user))
const password = reactive({ current: '', next: '', confirm: '' })
const passwordBusy = ref(false)
const passwordError = ref('')
const passwordSuccess = ref('')
const passwordValid = computed(() => mode.value !== 'unknown' && (mode.value !== 'current' || !!password.current)
  && Array.from(password.next).length >= 8 && password.next === password.confirm
  && (mode.value !== 'current' || password.next !== password.current))

const dialog = ref<'' | 'create' | 'edit' | 'secret' | 'toggle' | 'delete'>('')
const keyBusy = ref(false)
const dialogError = ref('')
const targetKey = ref<any>(null)
const keyForm = reactive({ name: '', expires: '', allow: [] as string[], sourceKey: '' })
const formAccess = ref<KeyAccess | null>(null)
const formAccessLoading = ref(false)
const formAccessError = ref('')
const formModels = computed(() => [...new Set<string>([...(formAccess.value?.models.map((model) => model.id) || []), ...(targetKey.value?.model_allow || []), ...keyForm.allow])])
const secret = ref('')
const secretName = ref('')
const secretCopied = ref(false)
const secretCopyBusy = ref(false)
const discardConfirm = ref(false)

const listContext = ref<PersonalContext>(requestContext(props.context))
const filters = reactive({ status: '', from: '', to: '', api_key_id: '', request_id: '' })
const filterError = ref('')
const page = computed(() => Number(listContext.value.page))
const hasNext = computed(() => records.value.length === 50)
function restoreFilters() {
  filters.status = listContext.value.status || ''
  filters.from = utcInput(listContext.value.from || '')
  filters.to = utcInput(listContext.value.to || '')
  filters.api_key_id = listContext.value.api_key_id || ''
  filters.request_id = listContext.value.request_id || ''
  filterError.value = ''
}

function clearPassword() {
  password.current = ''; password.next = ''; password.confirm = ''
  paneRoot.value?.querySelectorAll<HTMLInputElement>('input[type="password"]').forEach(input => { input.value = '' })
}
function clearDialog() {
  ++dialogGeneration; ++formAccessRead
  secret.value = ''; secretName.value = ''; secretCopied.value = false
  // Also wipe detached DOM references before Vue removes the secret panel.
  paneRoot.value?.querySelectorAll<HTMLTextAreaElement>('[data-testid="one-time-secret"]').forEach(input => { input.value = ''; input.textContent = '' })
  secretCopyBusy.value = false; discardConfirm.value = false
  keyForm.name = ''; keyForm.expires = ''; keyForm.allow = []; keyForm.sourceKey = ''
  targetKey.value = null; formAccess.value = null; formAccessError.value = ''; formAccessLoading.value = false
  dialogError.value = ''; dialog.value = ''
}
function requestClose() {
  if (dialog.value === 'secret' && secret.value) discardConfirm.value = true
  else clearDialog()
}
function reset() {
  ++lifetime; ++summaryRead; ++keysRead; ++requestsRead; ++accessRead; ++copyGeneration
  clearDialog(); clearPassword()
  summary.value = null; keys.value = []; records.value = []; access.value = null
  selectedKeyID.value = ''; selectedAlias.value = ''; selectedProtocol.value = ''
  summaryLoading.value = false; keysLoading.value = false; recordsLoading.value = false; accessLoading.value = false
  summaryError.value = ''; keysError.value = ''; recordsError.value = ''; accessError.value = ''
  actionSuccess.value = ''; passwordSuccess.value = ''; passwordError.value = ''; copyStatus.value = ''
  // Pending writes remain locked across a view/account switch; their payloads
  // cannot be cancelled, but their late results can never restore sensitive UI.
  listContext.value = requestContext(props.context); restoreFilters()
}

function ownedKeys(data: any[]): any[] {
  return data.filter((row) => row && Number.isSafeInteger(Number(row.id)) && Number(row.id) > 0
    && (row.user_id === undefined || String(row.user_id) === String(props.user?.id))).map((row) => ({
    id: Number(row.id), name: String(row.name || ''), active: row.active,
    key_prefix: String(row.key_prefix || ''), key_suffix: String(row.key_suffix || ''),
    expires_at: row.expires_at || null, last_used_at: row.last_used_at || null,
    model_allow: Array.isArray(row.model_allow) ? row.model_allow.filter((value: any) => typeof value === 'string') : [],
  }))
}
async function loadSummary() {
  const epoch = lifetime, read = ++summaryRead
  summaryLoading.value = true; summaryError.value = ''
  const result = await api.self.summary()
  if (!current(epoch) || read !== summaryRead) return
  if (result.error) { summary.value = null; summaryError.value = safeError(result) }
  else if (result.data && (!result.data.user?.id || String(result.data.user.id) === String(props.user?.id))) {
    summary.value = { user: result.data.user, usage: result.data.usage, limits: Array.isArray(result.data.limits) ? result.data.limits : [] }
  } else { summary.value = null; summaryError.value = '账号数据无效，请刷新重试' }
  summaryLoading.value = false
}
async function loadKeys(preferredID = '') {
  const epoch = lifetime, read = ++keysRead
  keysLoading.value = true; keysError.value = ''
  const result = await api.self.apiKeys()
  if (!current(epoch) || read !== keysRead) return
  if (result.error) { keys.value = []; keysError.value = safeError(result) }
  else if (Array.isArray(result.data)) keys.value = ownedKeys(result.data)
  else { keys.value = []; keysError.value = '密钥列表响应无效，请刷新重试' }
  const candidate = preferredID || selectedKeyID.value
  const nextID = keys.value.some((key) => String(key.id) === candidate) ? candidate : String(keys.value[0]?.id || '')
  const unchanged = selectedKeyID.value === nextID
  selectedKeyID.value = nextID
  keysLoading.value = false
  // A changed selection is loaded by the watcher, once. Refresh an unchanged
  // selection after edits to expiry/scope/active state too.
  if (props.view === 'myKeys' && nextID && unchanged) void loadAccess()
}
async function readAccess(id: number) {
  const self = api.self as typeof api.self & { access?: (id: number) => Promise<any> }
  return self.access ? self.access(id) : api.resource(`self/access?api_key_id=${encodeURIComponent(id)}`)
}
async function loadAccess() {
  const id = Number(selectedKeyID.value), epoch = lifetime, read = ++accessRead
  access.value = null; accessError.value = ''; selectedAlias.value = ''; selectedProtocol.value = ''
  if (props.view !== 'myKeys' || !selectedKey.value || !id) { accessLoading.value = false; return }
  accessLoading.value = true
  const result = await readAccess(id)
  if (!current(epoch) || read !== accessRead || Number(selectedKeyID.value) !== id) return
  if (result.error) accessError.value = safeError(result)
  else {
    access.value = normalizeAccess(result.data, id)
    if (!access.value) accessError.value = '接入能力响应无效，未推断任何协议或模型权限'
    selectedAlias.value = access.value?.models[0]?.id || ''
  }
  accessLoading.value = false
}
async function loadFormAccess() {
  const id = Number(keyForm.sourceKey), epoch = lifetime, modal = dialogGeneration, read = ++formAccessRead
  formAccess.value = null; formAccessError.value = ''
  if (!id || !keys.value.some((key) => key.id === id) || !['create', 'edit'].includes(dialog.value)) { formAccessLoading.value = false; return }
  formAccessLoading.value = true
  const result = await readAccess(id)
  if (!current(epoch) || modal !== dialogGeneration || read !== formAccessRead) return
  if (result.error) formAccessError.value = safeError(result)
  else {
    formAccess.value = normalizeAccess(result.data, id)
    if (!formAccess.value) formAccessError.value = '模型选项未确认，原白名单不会被自动改写'
  }
  formAccessLoading.value = false
}
async function loadRequests() {
  const epoch = lifetime, read = ++requestsRead
  recordsLoading.value = true; recordsError.value = ''; records.value = []; copyStatus.value = ''; ++copyGeneration
  // Use resource for compatibility with the legacy self.requests(number) helper.
  const result = await api.resource(`self/requests?${requestQuery(listContext.value)}`)
  if (!current(epoch) || read !== requestsRead) return
  if (result.error) recordsError.value = safeError(result)
  else if (Array.isArray(result.data)) {
    // Project safe display fields; never retain body, raw error summary, tags or upstream metadata.
    records.value = result.data.map((row: any) => ({
      id: storedRequestID(row), model_alias: row.model_alias, api_key_id: row.api_key_id,
      status: row.status, status_code: row.status_code, error_type: row.error_type, error_code: storedErrorCode(row),
      usage: { meters: { total: row.usage?.meters?.total, input: row.usage?.meters?.input, output: row.usage?.meters?.output }, estimated: row.usage?.estimated },
      cost: row.cost, credits: row.credits, total_latency_ms: row.total_latency_ms,
      created_at: row.created_at, settlement_status: row.settlement_status,
    }))
  } else recordsError.value = '请求列表响应无效，请刷新重试'
  recordsLoading.value = false
}
function loadView() {
  if (!props.user?.id) return
  if (props.view === 'account') void loadSummary()
  else if (props.view === 'myKeys') void loadKeys()
  else { void loadKeys(); void loadRequests() }
}

watch([() => props.view, () => props.user?.id, () => props.user?.username], () => { reset(); loadView() }, { immediate: true, flush: 'sync' })
watch(() => JSON.stringify(requestContext(props.context)), () => {
  if (props.view !== 'myRequests') return
  const next = requestContext(props.context)
  if (JSON.stringify(next) === JSON.stringify(listContext.value)) return
  listContext.value = next; restoreFilters(); void loadRequests()
})
watch(selectedKeyID, () => { if (props.view === 'myKeys') void loadAccess() }, { flush: 'sync' })
watch(selectedAlias, () => { selectedProtocol.value = availableProtocols.value[0] || '' }, { flush: 'sync' })
watch(() => keyForm.sourceKey, () => { void loadFormAccess() })
watch(mode, () => { clearPassword(); passwordError.value = '' })
onBeforeUnmount(() => { alive = false; reset() })

function openKey(row?: any) {
  if (keyBusy.value) return
  clearDialog(); actionSuccess.value = ''; copyStatus.value = ''
  targetKey.value = row || null
  dialog.value = row ? 'edit' : 'create'
  keyForm.name = row?.name || ''
  keyForm.expires = localInput(row?.expires_at)
  keyForm.allow = [...(row?.model_allow || [])]
  keyForm.sourceKey = String(row?.id || selectedKeyID.value || '')
}
function openAction(row: any, action: 'toggle' | 'delete') {
  if (keyBusy.value) return
  clearDialog(); actionSuccess.value = ''; targetKey.value = row; dialog.value = action
}
async function saveKey() {
  if (keyBusy.value || !['create', 'edit'].includes(dialog.value)) return
  dialogError.value = ''
  const name = keyForm.name.trim()
  if (!name || Array.from(name).length > 64) { dialogError.value = '密钥名称需为 1–64 个字符'; return }
  const creating = dialog.value === 'create'
  const expiryChanged = creating || keyForm.expires !== localInput(targetKey.value?.expires_at)
  let expires = ''
  if (expiryChanged && keyForm.expires) {
    const date = new Date(keyForm.expires)
    if (!Number.isFinite(date.getTime()) || date.getTime() <= Date.now()) { dialogError.value = '过期时间必须晚于当前时间'; return }
    expires = date.toISOString()
  }
  const epoch = lifetime, modal = dialogGeneration, id = targetKey.value?.id
  // Omit an unchanged expiry, preserving stored seconds and allowing edits to
  // an expired key without accidentally renewing it.
  const body = { name, model_allow: [...new Set(keyForm.allow)], ...(expiryChanged ? { expires_at: expires || null } : {}) }
  keyBusy.value = true
  try {
    // Explicit model_allow: [] means ALL enabled aliases. Never send user_id.
    // resource also works before createApiKey gains its third parameter.
    const result = creating
      ? await api.resource('self/api-keys', { method: 'POST', body: JSON.stringify({ name, model_allow: body.model_allow, ...(expires ? { expires_at: expires } : {}) }) })
      : await api.self.updateApiKey(id, body)
    if (!current(epoch)) return
    if (modal !== dialogGeneration) { void loadKeys(); return }
    if (result.error) { dialogError.value = safeError(result); return }
    if (creating) {
      if (typeof result.data?.secret !== 'string' || !result.data.secret) {
        dialogError.value = '密钥已签发但未收到一次性明文；请检查列表，必要时停用后重新创建'
        void loadKeys(); return
      }
      clearDialog()
      secret.value = result.data.secret; secretName.value = name; dialog.value = 'secret'
      void loadKeys(String(result.data.key?.id || ''))
    } else {
      clearDialog(); actionSuccess.value = '密钥已更新'; void loadKeys(String(id))
    }
  } finally { if (alive) keyBusy.value = false }
}
async function confirmAction() {
  if (keyBusy.value || !targetKey.value || !['toggle', 'delete'].includes(dialog.value)) return
  const epoch = lifetime, modal = dialogGeneration, row = targetKey.value, deleting = dialog.value === 'delete'
  keyBusy.value = true; dialogError.value = ''
  try {
    const result = deleting ? await api.self.deleteApiKey(row.id) : await api.self.updateApiKey(row.id, { active: row.active !== true })
    if (!current(epoch)) return
    if (modal !== dialogGeneration) { void loadKeys(); return }
    if (result.error) { dialogError.value = safeError(result); return }
    clearDialog(); actionSuccess.value = deleting ? '密钥已删除' : row.active === true ? '密钥已停用' : '密钥已启用（到期规则仍有效）'
    void loadKeys()
  } finally { if (alive) keyBusy.value = false }
}
async function copyValue(value: string, valid: () => boolean): Promise<boolean> {
  if (!valid()) return false
  try {
    if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(value); return valid() }
  } catch { /* fall back only if this view/dialog is still current */ }
  if (!valid()) return false
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const textarea = document.createElement('textarea')
  textarea.value = value; textarea.readOnly = true
  textarea.style.position = 'fixed'; textarea.style.opacity = '0'; textarea.style.pointerEvents = 'none'
  const container = previous?.closest('[role="dialog"]') || paneRoot.value || document.body
  container.appendChild(textarea)
  try { textarea.select(); return typeof document.execCommand === 'function' && document.execCommand('copy') }
  catch { return false }
  finally { textarea.value = ''; textarea.remove(); if (previous?.isConnected) previous.focus() }
}
async function copySecret() {
  if (!secret.value || dialog.value !== 'secret' || secretCopyBusy.value) return
  const epoch = lifetime, modal = dialogGeneration
  const valid = () => current(epoch) && modal === dialogGeneration && dialog.value === 'secret'
  secretCopyBusy.value = true; dialogError.value = ''
  const copied = await copyValue(secret.value, valid)
  if (!valid()) return
  secretCopyBusy.value = false; secretCopied.value = copied
  if (!copied) dialogError.value = '复制失败，请选中明文后手动复制，再确认已安全保存'
}
async function copyPublic(value: string) {
  if (!value) return
  const epoch = lifetime, generation = ++copyGeneration
  const valid = () => current(epoch) && generation === copyGeneration
  const copied = await copyValue(value, valid)
  if (valid()) copyStatus.value = copied ? '已复制' : '复制失败，请选中对应文本后手动复制'
}
async function changePassword() {
  if (passwordBusy.value || mode.value === 'unknown') return
  passwordError.value = ''; passwordSuccess.value = ''
  if (!passwordValid.value) { passwordError.value = '请核对原密码、新密码强度与两次输入是否一致'; return }
  const epoch = lifetime, sso = mode.value === 'sso'
  passwordBusy.value = true
  try {
    const result = await api.self.changePassword(sso ? '' : password.current, password.next)
    if (!current(epoch)) return
    clearPassword()
    if (result.error) { passwordError.value = safeError(result); return }
    passwordSuccess.value = sso ? '本地密码已设置；SSO 绑定保持不变，其他会话已失效' : '密码已更新；其他会话已失效，当前会话保留'
    emit('user-refresh')
    void loadSummary()
  } finally { if (alive) passwordBusy.value = false }
}
function applyContext(next: PersonalContext) {
  listContext.value = requestContext(next); restoreFilters()
  emit('context-change', { ...listContext.value })
  void loadRequests()
}
function applyFilters() {
  if (recordsLoading.value) return
  filterError.value = ''
  const from = filters.from ? inputUTC(filters.from) : '', to = filters.to ? inputUTC(filters.to) : ''
  if ((filters.from && !from) || (filters.to && !to)) { filterError.value = '时间格式无效，请使用 UTC 时间'; return }
  if (from && to && from > to) { filterError.value = '起始时间不能晚于结束时间'; return }
  if (filters.api_key_id && !/^[1-9]\d{0,14}$/.test(filters.api_key_id)) { filterError.value = '密钥 ID 无效'; return }
  if (filters.request_id && !/^[A-Za-z0-9_.:-]{1,160}$/.test(filters.request_id.trim())) { filterError.value = '请求 ID 格式无效，请粘贴实际记录 ID'; return }
  applyContext({ page: '1', status: filters.status, from, to, api_key_id: filters.api_key_id, request_id: filters.request_id.trim() })
}
function movePage(delta: number) {
  if (recordsLoading.value || (delta < 0 && page.value <= 1) || (delta > 0 && !hasNext.value)) return
  applyContext({ ...listContext.value, page: String(page.value + delta) })
}
function viewKeyRequests() {
  emit('navigate', { view: 'myRequests', context: { page: '1', api_key_id: selectedKeyID.value, status: 'success' } })
}
</script>

<template>
  <div ref="paneRoot" class="personal-pane">
    <div v-if="!user?.id" class="panel empty">请先登录后查看本人数据</div>
    <template v-else-if="view === 'account'">
      <section class="panel" aria-labelledby="personal-account-title">
        <div class="personal-heading"><h3 id="personal-account-title">账号中心 · {{ user.username }}</h3><button class="secondary" :disabled="summaryLoading" @click="loadSummary">刷新账号信息</button></div>
        <p class="muted">{{ user.role === 'admin' ? '管理员' : '普通用户' }} · 所有用量和规则仅属于当前账号</p>
        <p v-if="summaryLoading" role="status" class="state">正在加载账号用量…</p>
        <p v-else-if="summaryError" role="alert" class="error">{{ summaryError }}</p>
        <template v-else-if="summary">
          <dl class="personal-stats">
            <div><dt>请求数 / 成功 / 失败</dt><dd>{{ numberText(summary.usage?.requests) }} / {{ numberText(summary.usage?.success) }} / {{ numberText(summary.usage?.errors) }}</dd></div>
            <div><dt>总 Token（免费请求仍计量）</dt><dd>{{ numberText(summary.usage?.total_tokens) }} Token</dd></div>
            <div><dt>上游成本（CNY）</dt><dd>¥{{ summary.usage?.cost ?? '—' }}</dd></div>
            <div><dt>积分消耗</dt><dd>{{ summary.usage?.credits ?? '—' }} 积分</dd></div>
            <div><dt>成功率</dt><dd>{{ numberText(summary.usage?.success_rate) }}%</dd></div>
            <div><dt>平均耗时</dt><dd>{{ numberText(summary.usage?.avg_latency_ms) }} ms</dd></div>
          </dl>
          <h4>仍生效的限流规则</h4>
          <p v-if="!summary.limits.length" class="muted">没有返回账号或本人密钥的限流规则；不代表上游无限制。</p>
          <ul v-else class="personal-limits"><li v-for="limit in summary.limits" :key="limit.id">{{ limit.scope === 'key' ? `密钥 #${limit.scope_id}` : '账号' }} · {{ limit.model_id || '全部模型' }} · RPM {{ limit.rpm }} · TPM {{ limit.tpm }} · 并发 {{ limit.concurrency }} · {{ limit.active === false ? '已停用' : '启用' }}</li></ul>
        </template>
        <div class="personal-actions"><button class="secondary" @click="emit('navigate', { view: 'myKeys' })">我的密钥与快速接入</button><button class="secondary" @click="emit('navigate', { view: 'myRequests', context: { page: '1' } })">我的请求记录</button></div>
      </section>
      <section class="panel password-panel" aria-labelledby="personal-password-title">
        <h3 id="personal-password-title">{{ mode === 'sso' ? '设置本地密码' : '修改本地密码' }}</h3>
        <p v-if="mode === 'unknown'" class="muted">密码能力未知或尚未允许设置，已禁止提交。请刷新账号信息或联系管理员；不会推断你有本地密码。</p>
        <p v-else-if="mode === 'sso'" class="muted">此账号目前仅使用 SSO。设置后将开启本地密码登录，SSO 仍可使用且原绑定不变；不需要原密码，不修改身份源密码。</p>
        <p v-else class="muted">已有本地密码，必须验证当前密码。此操作不修改 SSO 身份源密码。</p>
        <p class="muted">新密码至少 8 个字符，建议使用更长的独立密码。成功后其他设备 / 会话将失效，当前会话保留；这里不是密码找回或重置入口。</p>
        <form data-testid="password-form" @submit.prevent="changePassword">
          <div class="form-grid">
            <label v-if="mode === 'current'">当前密码<input v-model="password.current" data-testid="current-password" type="password" autocomplete="current-password" :disabled="passwordBusy" required /></label>
            <label>新密码<input v-model="password.next" data-testid="new-password" type="password" autocomplete="new-password" :disabled="passwordBusy || mode === 'unknown'" required /></label>
            <label>确认新密码<input v-model="password.confirm" data-testid="confirm-password" type="password" autocomplete="new-password" :disabled="passwordBusy || mode === 'unknown'" required /></label>
          </div>
          <p v-if="passwordError" role="alert" class="error">{{ passwordError }}</p>
          <p v-if="passwordSuccess" role="status" class="personal-success">{{ passwordSuccess }}</p>
          <div class="personal-actions"><button class="primary compact" :disabled="passwordBusy || !passwordValid">{{ passwordBusy ? '提交中…' : mode === 'sso' ? '确认开启本地密码登录' : '确认修改密码' }}</button><button type="button" class="secondary" :disabled="passwordBusy" @click="clearPassword">清空密码输入</button></div>
        </form>
      </section>
    </template>

    <template v-else-if="view === 'myKeys'">
      <section class="panel" aria-labelledby="personal-keys-title">
        <div class="personal-heading"><h3 id="personal-keys-title">我的 API 密钥</h3><div class="personal-actions"><button class="secondary" :disabled="keysLoading" @click="loadKeys()">刷新密钥</button><button class="primary compact" data-testid="new-key" :disabled="keyBusy" @click="openKey()">新建本人密钥</button></div></div>
        <p class="muted">归属固定为当前账号 {{ user.username }}；管理员和普通用户都只能在此为本人签发。列表永不再次读取明文。</p>
        <p v-if="actionSuccess" role="status" class="personal-success">{{ actionSuccess }}</p>
        <p v-if="keysLoading" role="status" class="state">正在加载本人密钥…</p>
        <div v-else-if="keysError" role="alert" class="error">{{ keysError }} <button class="secondary" @click="loadKeys()">重试读取</button></div>
        <p v-else-if="!keys.length" class="state">尚无本人密钥。创建后请立即安全保存一次性明文。</p>
        <div v-else class="key-list">
          <article v-for="key in keys" :key="key.id" class="key-row" :data-key-id="key.id">
            <div><h4>{{ key.name }}</h4><code>{{ key.key_prefix }}…{{ key.key_suffix }}</code><span class="tag" :class="{ inactive: keyStatus(key) !== '有效' }">{{ keyStatus(key) }}</span></div>
            <dl class="key-details"><div><dt>到期时间</dt><dd>{{ key.expires_at ? utcTime(key.expires_at) : '永不过期' }}</dd></div><div><dt>模型范围</dt><dd>{{ key.model_allow.length ? `白名单：${key.model_allow.join('、')}` : '空白名单：全部启用的模型别名' }}</dd></div><div><dt>最后使用（不代表成功）</dt><dd>{{ key.last_used_at ? utcTime(key.last_used_at) : '从未使用' }}</dd></div></dl>
            <div class="personal-actions"><button class="secondary" :disabled="keyBusy" @click="openKey(key)">编辑</button><button class="secondary" :class="{ danger: key.active === true }" :disabled="keyBusy" @click="openAction(key, 'toggle')">{{ key.active === true ? '停用' : '启用' }}</button><button class="secondary danger" :disabled="keyBusy" @click="openAction(key, 'delete')">删除</button></div>
          </article>
        </div>
      </section>
      <section class="panel" aria-labelledby="personal-access-title">
        <div class="personal-heading"><h3 id="personal-access-title">快速接入</h3><button class="secondary" :disabled="!selectedKey || accessLoading" @click="loadAccess">刷新接入状态</button></div>
        <p class="muted">只读取本人所选密钥的模型与能力元数据。访问页面、复制或刷新不会调用模型，也不会读取上游地址或凭据。</p>
        <label class="base-url">Base URL（SDK 基础地址，已包含 /v1）<div class="copy-line"><input :value="baseURL" aria-label="Base URL" readonly /><button class="secondary" @click="copyPublic(baseURL)">复制 Base URL</button></div></label>
        <label>本人密钥<select v-model="selectedKeyID" data-testid="access-key" :disabled="!keys.length"><option v-if="!keys.length" value="">请先创建本人密钥</option><option v-for="key in keys" :key="key.id" :value="String(key.id)">{{ key.name }} · {{ key.key_prefix }}…{{ key.key_suffix }} · {{ keyStatus(key) }}</option></select></label>
        <p v-if="accessLoading" role="status" class="state">正在查询所选密钥接入能力…</p>
        <p v-else-if="accessError" role="alert" class="error">{{ accessError }}</p>
        <template v-else-if="access">
          <p v-if="!accessUsable" role="status" class="error">{{ access.key_status === 'expired' || keyStatus(selectedKey) === '已过期' ? '所选密钥已过期，请由本人编辑到期时间或创建新密钥。' : access.key_status === 'inactive' || keyStatus(selectedKey) === '已停用' ? '所选密钥已停用，不能调用；可由本人手动启用。' : '所选密钥状态未确认，暂不提供可调用示例。' }}</p>
          <p class="first-success" data-testid="first-success">{{ access.first_success_at ? `首次成功：${utcTime(access.first_success_at)}` : '尚无此密钥的成功记录' }}。依据服务端保存的本人成功请求，不以最后使用推断。</p>
          <button class="secondary" @click="viewKeyRequests">查看此密钥成功记录</button>
          <p v-if="!access.models.length" class="state">该密钥没有返回启用的可访问模型；请检查白名单。不会据此推断或扩大授权。</p>
          <template v-else>
            <label>网关模型别名<select v-model="selectedAlias" data-testid="access-alias"><option v-for="model in access.models" :key="model.id" :value="model.id">{{ model.id }}</option></select></label>
            <div v-if="selectedModel" class="model-capabilities">
              <p>{{ pricingLabel(selectedModel.pricing_status) }} · {{ selectedModel.ready ? '路由配置就绪（不保证上游实时可用）' : '模型尚未就绪' }}</p>
              <p v-if="!selectedModel.ready" class="muted">{{ selectedModel.reasons.length ? '有效路由或配置条件不满足，请联系管理员检查。' : '服务器未确认模型已就绪，请联系管理员。' }}</p>
              <p v-if="selectedModel.capabilities === null || !Object.keys(selectedModel.capabilities).length" class="muted">能力未知：尚无可确认的能力声明，不能假定支持工具、流式或其他功能。</p>
              <ul v-else class="capabilities"><li v-for="(enabled, name) in selectedModel.capabilities" :key="name">{{ capabilityLabel(String(name)) }}：{{ enabled ? '已确认支持' : '已确认不支持' }}</li></ul>
              <p class="muted">未明确声明的能力均为未知；不会把某一协议可用推断为所有能力可用。</p>
              <p v-if="!availableProtocols.length" class="muted">没有可确认的已知协议，暂不生成调用示例。</p>
              <label v-else>已确认协议<select v-model="selectedProtocol" data-testid="access-protocol"><option v-for="protocol in availableProtocols" :key="protocol" :value="protocol">{{ protocols[protocol].label }}</option></select></label>
              <p v-if="selectedModel.protocols.some(protocol => !Object.prototype.hasOwnProperty.call(protocols, protocol))" class="muted">存在尚未识别的协议声明，不会将其当作 OpenAI 兼容能力。</p>
            </div>
            <div v-if="example" class="safe-examples">
              <p>完整请求 URL（不是 SDK Base URL）：<code>{{ example.url }}</code></p>
              <p class="muted">请在客户端 / 本机终端手动设置 GATEWAY_API_KEY。运行下列示例才会产生请求；页面没有试调用按钮。</p>
              <div class="snippet-heading"><h4>curl（需本机 Python 3 安全编码 JSON）</h4><button class="secondary" @click="copyPublic(example.curl)">复制 curl 示例</button></div><pre data-testid="curl-example"><code>{{ example.curl }}</code></pre>
              <div class="snippet-heading"><h4>{{ selectedProtocol === 'messages' ? 'Python（Messages 标准库）' : 'OpenAI Python SDK' }}</h4><button class="secondary" @click="copyPublic(example.python)">复制 Python 示例</button></div><pre data-testid="python-example"><code>{{ example.python }}</code></pre>
            </div>
          </template>
        </template>
        <p v-if="copyStatus" role="status" class="muted">{{ copyStatus }}</p>
      </section>
      <section class="panel rotation-guide" aria-labelledby="personal-rotation-title">
        <h3 id="personal-rotation-title">本人手动轮换</h3>
        <ol><li>由本人明确新建一把密钥并保存明文（不自动签发）。</li><li>手动将客户端迁移到新密钥；不要将密钥填入 URL。</li><li>选择新密钥，核对上方服务端的「首次成功」或本人成功记录；最后使用不代表验证成功。</li><li>确认迁移后，回到旧密钥这一行手动停用。不会自动停旧、删除或迁移历史记录。</li></ol>
        <button class="secondary" :disabled="keyBusy" @click="openKey()">新建轮换密钥</button>
      </section>
    </template>

    <section v-else class="panel" aria-labelledby="personal-requests-title">
      <div class="personal-heading"><h3 id="personal-requests-title">我的请求记录</h3><button class="secondary" :disabled="recordsLoading" @click="loadRequests">刷新记录</button></div>
      <p class="muted">仅本人的安全摘要，不展示请求或响应正文。时间显示与筛选均为 UTC；起止时间包含边界。请求结果与结算状态分别记录。</p>
      <form class="request-filters" data-testid="request-filters" @submit.prevent="applyFilters">
        <label>请求结果<select v-model="filters.status" data-testid="filter-status"><option value="">全部结果</option><option value="success">成功</option><option value="error">失败</option></select></label>
        <label>起始时间（UTC）<input v-model="filters.from" data-testid="filter-from" type="datetime-local" step="0.001" /></label>
        <label>结束时间（UTC）<input v-model="filters.to" data-testid="filter-to" type="datetime-local" step="0.001" /></label>
        <label>本人密钥<select v-model="filters.api_key_id" data-testid="filter-key"><option value="">全部本人密钥</option><option v-if="filters.api_key_id && !keys.some(key => String(key.id) === filters.api_key_id)" :value="filters.api_key_id">密钥 #{{ filters.api_key_id }}（未在当前列表中，仍由服务器校验归属）</option><option v-for="key in keys" :key="key.id" :value="String(key.id)">{{ key.name }} · #{{ key.id }}</option></select></label>
        <label>请求 ID（精确）<input v-model="filters.request_id" data-testid="filter-request-id" type="text" maxlength="160" /></label>
        <div class="personal-actions"><button class="primary compact" :disabled="recordsLoading">应用筛选</button><button type="button" class="secondary" :disabled="recordsLoading" @click="applyContext({ page: '1' })">清除筛选</button></div>
      </form>
      <p v-if="filterError" role="alert" class="error">{{ filterError }}</p>
      <p v-if="keysError" role="alert" class="error">密钥筛选选项加载失败：{{ keysError }}</p>
      <p v-if="recordsLoading" role="status" class="state">正在加载本人请求记录…</p>
      <div v-else-if="recordsError" role="alert" class="error">{{ recordsError }} <button class="secondary" @click="loadRequests">重试读取</button></div>
      <p v-else-if="!records.length" class="state">当前页和筛选条件没有请求记录。</p>
      <div v-else class="request-list">
        <article v-for="(record, index) in records" :key="record.id || index" class="request-row" :data-request-id="record.id">
          <div class="request-top"><strong>{{ record.model_alias || '未记录模型别名' }}</strong><span class="tag" :class="{ inactive: record.status !== 'success' }">{{ record.status === 'success' ? '成功' : record.status === 'error' ? '失败' : '结果未知' }}</span><span>HTTP {{ record.status_code || '未记录' }}</span><time>{{ utcTime(record.created_at) }}</time></div>
          <p class="request-usage">{{ numberText(record.usage?.meters?.total) }} Token · {{ record.usage?.estimated === true ? '估算用量' : record.usage?.estimated === false ? '实际用量' : '用量来源未标记' }} · ¥{{ record.cost ?? '—' }} CNY · {{ record.credits ?? '—' }} 积分 · {{ numberText(record.total_latency_ms) }} ms</p>
          <p class="settlement">结算：{{ settlementLabel(record.settlement_status) }}</p>
          <p v-if="record.status !== 'success'" class="diagnosis"><b>{{ diagnosis(record).type }}</b> · {{ diagnosis(record).reason }}<br />建议：{{ diagnosis(record).next }}</p>
          <p v-if="record.error_code" class="muted">code: {{ record.error_code }}</p>
          <div class="request-id"><code>{{ record.id || '未记录请求 ID' }}</code><button v-if="record.id" class="secondary" @click="copyPublic(record.id)">复制请求 ID</button></div>
        </article>
      </div>
      <div class="pagination"><button class="secondary" data-testid="previous-page" :disabled="recordsLoading || page <= 1" @click="movePage(-1)">上一页</button><span>第 {{ page }} 页 · 每页 50 条</span><button class="secondary" data-testid="next-page" :disabled="recordsLoading || !hasNext" @click="movePage(1)">下一页</button></div>
      <p v-if="copyStatus" role="status" class="muted">{{ copyStatus }}</p>
    </section>

    <PersonalDialog v-if="dialog === 'create' || dialog === 'edit'" :key="dialog" :title="dialog === 'create' ? '新建本人 API 密钥' : '编辑本人 API 密钥'" label-id="personal-key-dialog-title" @close="requestClose">
      <form data-testid="key-form" @submit.prevent="saveKey">
        <p class="muted">固定归属当前会话账号，不能代他人签发或转移归属。</p>
        <div class="form-grid"><label>名称<input v-model="keyForm.name" data-testid="key-name" data-autofocus type="text" maxlength="64" :disabled="keyBusy" required /></label><label>到期时间（{{ localTimezone }}）<input v-model="keyForm.expires" data-testid="key-expiry" type="datetime-local" :disabled="keyBusy" /><small>留空为永不过期；提交时转换为 UTC。</small></label></div>
        <label v-if="keys.length">模型选项来源（本人密钥）<select v-model="keyForm.sourceKey" data-testid="scope-source" :disabled="keyBusy"><option v-for="key in keys" :key="key.id" :value="String(key.id)">{{ key.name }} · #{{ key.id }}</option></select></label>
        <fieldset :disabled="keyBusy"><legend>模型别名白名单</legend><p class="muted">显式空白名单 = 全部启用的模型别名，不是「无权访问」。选项来自本人密钥接入信息，未列出别名不代表额外授予或撤销权限。</p><p v-if="formAccessLoading" role="status">正在查询本人密钥的模型选项…</p><p v-else-if="formAccessError" role="alert" class="error">{{ formAccessError }}</p><p v-else-if="!formModels.length" class="muted">尚无本人密钥模型选项。可先以空白名单创建，再查询本人接入信息并编辑；不会读取管理端模型。</p><label v-for="alias in formModels" :key="alias" class="check"><input v-model="keyForm.allow" :value="alias" type="checkbox" />{{ alias }}<small v-if="!formAccess?.models.some(model => model.id === alias)">（原白名单，当前选项未确认）</small></label><button v-if="keyForm.allow.length" type="button" class="secondary" @click="keyForm.allow = []">清空白名单（全部启用别名）</button></fieldset>
        <p v-if="keyBusy" class="muted">请求正在提交。关闭或切换页面会丢弃迟到的一次性明文；可回到列表检查并手动停用后重新创建。</p>
        <p v-if="dialogError" role="alert" class="error">{{ dialogError }}</p>
        <div class="personal-actions"><button type="button" class="secondary" @click="requestClose">取消</button><button class="primary compact" :disabled="keyBusy">{{ keyBusy ? '提交中…' : dialog === 'create' ? '签发本人密钥' : '保存编辑' }}</button></div>
      </form>
    </PersonalDialog>
    <PersonalDialog v-else-if="dialog === 'secret'" key="secret" title="密钥已创建 · 仅显示这一次" label-id="personal-secret-title" @close="requestClose">
      <p>{{ secretName }}：请立即保存到你自己的安全凭据管理工具。关闭、退出或切换账号 / 页面后将清除，无法再次查询。</p>
      <label class="secret-label">一次性明文<textarea :value="secret" data-testid="one-time-secret" class="secret-value" aria-label="一次性密钥明文" readonly spellcheck="false" @focus="($event.target as HTMLTextAreaElement).select()" /></label>
      <p v-if="secretCopied" role="status" class="personal-success">已复制，请确认已经安全保存；剪贴板不等于长期保存。</p>
      <p v-if="dialogError" role="alert" class="error">{{ dialogError }}</p>
      <template v-if="discardConfirm"><p role="alert" class="error">确定丢弃未确认保存的明文吗？清除后无法再次显示。</p><div class="personal-actions"><button class="secondary" @click="discardConfirm = false">继续保存</button><button class="secondary danger" @click="clearDialog">放弃并清除明文</button></div></template>
      <div v-else class="personal-actions"><button class="secondary" data-autofocus :disabled="secretCopyBusy" @click="copySecret">{{ secretCopyBusy ? '复制中…' : '复制密钥' }}</button><button class="primary compact" @click="clearDialog">我已安全保存</button></div>
    </PersonalDialog>
    <PersonalDialog v-else-if="dialog === 'toggle' || dialog === 'delete'" :key="dialog" :title="dialog === 'delete' ? '确认删除本人密钥' : targetKey?.active === true ? '确认停用本人密钥' : '确认启用本人密钥'" label-id="personal-key-action-title" @close="requestClose">
      <p>目标：{{ targetKey?.name }} · {{ targetKey?.key_prefix }}…{{ targetKey?.key_suffix }}</p>
      <p class="muted">{{ dialog === 'delete' ? '删除不可恢复，使用此密钥的客户端将无法再鉴权；历史请求不会转移给新密钥。' : targetKey?.active === true ? '停用立即阻止使用此密钥的客户端调用。轮换前请先人工确认新密钥的成功记录。' : '启用不等于续期；已过期的密钥仍不能调用，请由本人另行编辑到期时间。' }}</p>
      <p v-if="dialogError" role="alert" class="error">{{ dialogError }}</p>
      <div class="personal-actions"><button class="secondary" @click="requestClose">取消</button><button class="primary compact" data-testid="confirm-key-action" :disabled="keyBusy" @click="confirmAction">{{ keyBusy ? '提交中…' : '确认操作' }}</button></div>
    </PersonalDialog>
  </div>
</template>

<style scoped>
.personal-pane { display: grid; gap: 20px; min-width: 0; }
.personal-heading, .snippet-heading { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; margin-bottom: 10px; }
.personal-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 12px; }
.personal-heading .personal-actions { margin-top: 0; }
.personal-stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; margin: 20px 0; }
.personal-stats > div { border: 1px solid var(--border); border-radius: 8px; padding: 14px; background: var(--surface-muted); }
.personal-stats dt, .key-details dt { font-size: 12px; color: var(--text-muted); }
.personal-stats dd { margin: 8px 0 0; font-size: 18px; overflow-wrap: anywhere; }
h4 { font-size: 13px; margin: 12px 0 6px; }
.personal-limits, .rotation-guide ol { margin: 12px 0; padding-left: 22px; font-size: 13px; line-height: 1.8; }
.state { color: var(--text-muted); padding: 24px 0; font-size: 13px; }
.personal-success { color: var(--success); padding: 10px 0; font-size: 13px; }
.key-list, .request-list { display: grid; margin-top: 14px; }
.key-row, .request-row { border-top: 1px solid var(--border); padding: 16px 0; overflow-wrap: anywhere; }
.key-row h4 { margin-top: 0; }
.key-row .tag { margin-left: 12px; }
.key-details { display: grid; grid-template-columns: 1fr 1.3fr 1fr; gap: 14px; margin: 12px 0; }
.key-details dd { margin: 5px 0 0; font-size: 12px; line-height: 1.6; }
.copy-line { display: flex; gap: 8px; align-items: center; }
.copy-line input { min-width: 0; }
.copy-line button { flex-shrink: 0; }
.base-url { margin: 16px 0; }
.personal-pane label + label, .model-capabilities, .first-success { margin-top: 14px; }
.first-success, .model-capabilities, .safe-examples { font-size: 13px; line-height: 1.7; }
.capabilities { padding-left: 20px; }
.safe-examples { margin-top: 18px; }
.safe-examples code { overflow-wrap: anywhere; }
pre { margin: 8px 0 18px; max-width: 100%; overflow: auto; border: 1px solid var(--border); border-radius: 8px; padding: 14px; background: var(--surface-sunken); color: var(--text); font-size: 12px; }
.request-filters { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin: 18px 0; align-items: end; }
.request-filters label + label { margin-top: 0; }
.request-top { display: flex; gap: 12px; flex-wrap: wrap; align-items: center; font-size: 12px; }
.request-top time { color: var(--text-muted); }
.request-usage, .settlement, .diagnosis { font-size: 12px; line-height: 1.8; margin-top: 8px; }
.request-id { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; margin-top: 10px; font-size: 12px; }
.pagination { display: flex; justify-content: center; align-items: center; flex-wrap: wrap; gap: 12px; margin-top: 18px; font-size: 12px; }
fieldset { margin: 16px 0; padding: 14px; border: 1px solid var(--border); border-radius: 8px; }
legend { font-size: 12px; color: var(--text-muted); }
fieldset .check { margin: 12px 0; }
.secret-label { margin-top: 14px; }
.secret-value { min-height: 78px; }
button:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
@media (max-width: 900px) { .personal-stats, .request-filters { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 600px) { .personal-pane > .panel { padding: 16px; } .personal-stats, .key-details, .request-filters { grid-template-columns: 1fr; } .copy-line { flex-wrap: wrap; } .personal-heading { align-items: flex-start; } }
</style>
