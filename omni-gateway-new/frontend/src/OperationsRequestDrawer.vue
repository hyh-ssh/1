<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from './api'
import { localizeError } from './errors'

type Row = Record<string, any>
const props = defineProps<{ requestId: string }>()
const emit = defineEmits<{
  close: []
  navigate: [target: { view: string; context?: Record<string, string> }]
}>()
const dialog = ref<HTMLElement | null>(null), closeButton = ref<HTMLButtonElement | null>(null)
const detail = ref<Row | null>(null), loading = ref(false), error = ref('')
const consent = ref(false), content = ref<Row | null>(null), contentLoading = ref(false), contentError = ref(''), contentRead = ref(false)
const copyNotice = ref('')
let detailGeneration = 0, contentGeneration = 0, alive = true
let returnFocus: HTMLElement | null = null, previousOverflow = ''
let detailAbort: AbortController | null = null, contentAbort: AbortController | null = null

function resultError(result: any): string {
  return [localizeError(String(result.error)), result.status ? `HTTP ${result.status}` : '', result.code ? `错误码：${result.code}` : '', result.request_id ? `诊断请求 ID：${result.request_id}` : ''].filter(Boolean).join(' · ')
}
function clearSensitive() {
  ++contentGeneration
  contentAbort?.abort(); contentAbort = null
  consent.value = false; content.value = null; contentLoading.value = false; contentError.value = ''; contentRead.value = false
}
async function loadDetail() {
  const current = ++detailGeneration, id = props.requestId
  detailAbort?.abort(); detailAbort = new AbortController()
  clearSensitive(); detail.value = null; error.value = ''; copyNotice.value = ''; loading.value = true
  try {
    const result = await api.resource(`admin/requests/${encodeURIComponent(id)}`, { signal: detailAbort.signal })
    if (!alive || current !== detailGeneration || props.requestId !== id) return
    if (result.error) error.value = resultError(result)
    else if (!result.data?.request) error.value = '请求详情不可用或链接已失效。'
    else detail.value = result.data
  } catch { if (alive && current === detailGeneration) error.value = '加载失败，请重试读取请求详情。' }
  finally { if (alive && current === detailGeneration) loading.value = false }
}
watch(() => props.requestId, () => { void loadDetail() }, { immediate: true })
async function revealContent() {
  if (!consent.value || contentRead.value || contentLoading.value || !detail.value) return
  const current = ++contentGeneration, id = props.requestId
  contentRead.value = true; contentLoading.value = true; contentError.value = ''
  contentAbort = new AbortController()
  try {
    const result = await api.resource(`admin/content/${encodeURIComponent(id)}`, { signal: contentAbort.signal })
    if (!alive || current !== contentGeneration || id !== props.requestId) return
    if (result.error) contentError.value = resultError(result)
    else content.value = result.data || null
  } catch { if (alive && current === contentGeneration) contentError.value = '内容日志读取失败；此次读取已提交，不会自动重复读取。' }
  finally { if (alive && current === contentGeneration) contentLoading.value = false }
}
function close() {
  ++detailGeneration; detailAbort?.abort(); detail.value = null; loading.value = false
  clearSensitive(); emit('close')
}
function focusable(): HTMLElement[] {
  return Array.from(dialog.value?.querySelectorAll<HTMLElement>('button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex="0"]') || [])
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); return }
  if (event.key !== 'Tab') return
  const items = focusable(), first = items[0], last = items[items.length - 1]
  if (!first || !last) { event.preventDefault(); dialog.value?.focus(); return }
  if (!dialog.value?.contains(document.activeElement) || (event.shiftKey && document.activeElement === first)) { event.preventDefault(); (event.shiftKey ? last : first).focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
function containFocus(event: FocusEvent) {
  if (dialog.value && !dialog.value.contains(event.target as Node)) closeButton.value?.focus({ preventScroll: true })
}
onMounted(() => {
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  previousOverflow = document.body.style.overflow; document.body.style.overflow = 'hidden'
  document.addEventListener('keydown', keydown, true); document.addEventListener('focusin', containFocus)
  void nextTick(() => closeButton.value?.focus({ preventScroll: true }))
})
onBeforeUnmount(() => {
  alive = false; ++detailGeneration; detailAbort?.abort(); clearSensitive(); detail.value = null
  document.removeEventListener('keydown', keydown, true); document.removeEventListener('focusin', containFocus)
  document.body.style.overflow = previousOverflow
  if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true })
  returnFocus = null
})
const request = computed<Row>(() => detail.value?.request || {})
const time = (value: string) => value && Number.isFinite(Date.parse(value)) ? `${new Date(value).toISOString().replace('T', ' ').replace('Z', '')} UTC` : '—'
const json = (value: unknown) => JSON.stringify(value, null, 2)
function statusLabel(value: string): string {
  return ({ success: '成功', error: '失败', failed: '失败', pending: '处理中', cancelled: '已取消' } as Record<string, string>)[value] || (value ? `未知（${value}）` : '未记录')
}
function settlementLabel(value: string): string {
  return ({ settled: '已结算', free: '免费（显式零费率）', credits_free: '免积分（成本独立计量）', unpriced: '未定价（非免费）', failed: '结算失败', none: '无需结算' } as Record<string, string>)[value] || (value ? `未知（${value}）` : '未记录（历史记录）')
}
function settlementHint(value: string): string {
  if (value === 'unpriced') return '未配置价格版本，本次未计费；若要表达免费，请在「价格」中创建成本和积分均为 0 的零费率版本。未定价只提示，不阻止调用。'
  return `结算异常（${value}），需核对账本与价格配置`
}
const settlement = computed(() => request.value.settlement_status || (detail.value?.attempts?.some((a: Row) => a.settlement_error && a.settlement_error !== 'unpriced') ? 'failed' : detail.value?.attempts?.some((a: Row) => a.settlement_error === 'unpriced') ? 'unpriced' : ''))
async function copyId() {
  try { await navigator.clipboard.writeText(String(request.value.id || props.requestId)); copyNotice.value = '请求 ID 已复制' }
  catch { copyNotice.value = '无法访问剪贴板，请手动复制请求 ID。' }
}
</script>

<template>
  <div class="request-overlay" @click.self="close">
    <section ref="dialog" class="request-drawer" role="dialog" aria-modal="true" aria-labelledby="operations-request-title" tabindex="-1">
      <header class="drawer-header"><div><h3 id="operations-request-title">请求详情</h3><p class="muted request-id">{{ requestId }}</p></div><button ref="closeButton" class="secondary" type="button" aria-label="关闭请求详情" @click="close">关闭明细</button></header>
      <div class="drawer-body">
        <div v-if="loading" class="detail-skeleton" role="status" aria-busy="true"><p>正在加载请求详情…</p><div v-for="n in 5" :key="n" class="skeleton-line" /></div>
        <div v-else-if="error" class="detail-error"><p class="alert" role="alert">{{ error }}</p><button type="button" class="secondary" @click="loadDetail">重试读取详情</button></div>
        <div v-else-if="detail" class="request-detail">
          <p class="request-id">请求 ID：{{ request.id || requestId }} <button type="button" class="secondary compact" @click="copyId">复制请求 ID</button></p><p v-if="copyNotice" class="muted" role="status">{{ copyNotice }}</p>
          <dl class="request-facts"><div><dt>请求结果</dt><dd>{{ statusLabel(request.status) }}<span v-if="request.status_code"> · HTTP {{ request.status_code }}</span></dd></div><div><dt>结算状态</dt><dd>{{ settlementLabel(settlement) }}</dd></div><div><dt>时间（UTC）</dt><dd>{{ time(request.created_at) }}</dd></div><div><dt>用户 / 密钥</dt><dd>{{ request.user_id }} / {{ request.api_key_id }}</dd></div><div><dt>模型 / 协议</dt><dd>{{ request.model_alias || '—' }} / {{ request.api || '—' }}</dd></div><div><dt>耗时 / 首字</dt><dd>{{ request.total_latency_ms }} ms / {{ request.ttft_ms }} ms</dd></div><div><dt>成本（CNY） / 积分</dt><dd>¥{{ request.cost ?? '—' }} / {{ request.credits ?? '—' }}</dd></div></dl>
          <p v-if="request.error_summary" class="error">{{ localizeError(request.error_summary) }}</p><p v-if="request.diagnosis" class="muted">建议：{{ localizeError(request.diagnosis) }}</p><p v-if="request.error_code" class="muted">错误码：{{ request.error_code }}</p>
          <h3>上游尝试</h3>
          <p v-if="!detail.attempts?.length" class="muted">无上游尝试记录</p>
          <div v-for="(attempt, index) in detail.attempts || []" :key="attempt.settlement_id || index" class="route-row attempt-row">
            <strong>{{ attempt.provider_model }} <span class="muted">· 供应商 {{ attempt.provider_id ?? '—' }}</span></strong><p>{{ statusLabel(attempt.status) }} · HTTP {{ attempt.status_code ?? '—' }} · {{ attempt.latency_ms }} ms · 成本 ¥{{ attempt.cost ?? '—' }} · 积分 {{ attempt.credits ?? '—' }}</p>
            <p v-if="attempt.settlement_status" class="muted">{{ settlementLabel(attempt.settlement_status) }}</p><p v-if="attempt.settlement_error" class="error">{{ settlementHint(attempt.settlement_error) }}</p><p v-if="attempt.error_summary" class="error">{{ localizeError(attempt.error_summary) }}</p>
            <button v-if="attempt.settlement_error || attempt.settlement_status === 'unpriced'" type="button" class="secondary compact" @click="emit('navigate', { view: 'prices', context: { model: attempt.provider_model || request.model_alias || '' } })">检查价格配置</button>
          </div>
          <h3>用量与不可变账本</h3><p class="muted">零金额仍保留计量数量与价格版本；估算项按记录标记，不根据金额推断免费。</p><pre>{{ json(request.usage || {}) }}</pre>
          <div class="scroll"><table><thead><tr><th>尝试</th><th>计量项</th><th>数量</th><th>成本（CNY）</th><th>积分</th><th>价格版本</th></tr></thead><tbody><tr v-for="entry in detail.ledger || []" :key="entry.id"><td :title="entry.settlement_id">{{ entry.settlement_id?.slice(0, 8) }}</td><td>{{ entry.meter }}{{ entry.estimated ? '（估算）' : '' }}</td><td>{{ entry.quantity }}</td><td>{{ entry.cost }}</td><td>{{ entry.credits }}</td><td>{{ entry.price_version }}</td></tr><tr v-if="!detail.ledger?.length"><td colspan="6" class="muted">无账本记录</td></tr></tbody></table></div>
          <section class="content-section" aria-label="内容日志"><h3>内容日志（单独确认）</h3><p class="muted">正文默认不保存；仅启用内容日志时可查看。每次读取会记入审计日志，可能包含敏感信息；关闭详情即清理。</p><label class="check"><input v-model="consent" type="checkbox" :disabled="contentRead" />我了解正文可能包含敏感信息，并同意此次审计读取</label><button type="button" class="secondary" :disabled="!consent || contentLoading || contentRead" @click="revealContent">{{ contentLoading ? '读取中…' : contentRead ? '此次内容日志已读取' : '查看内容日志' }}</button><p v-if="contentLoading" role="status" class="muted">正在读取内容日志…</p><p v-if="contentError" class="alert" role="alert">{{ contentError }}</p><pre v-if="content" class="content-log">{{ json(content) }}</pre></section>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.request-overlay{position:fixed;inset:0;z-index:1000;background:rgba(0,0,0,.35);display:flex;justify-content:flex-end}.request-drawer{height:100%;width:min(880px,64vw);min-width:540px;background:var(--surface,#fff);color:var(--text);border-left:1px solid var(--border);box-shadow:-12px 0 40px rgba(0,0,0,.15);display:flex;flex-direction:column;outline:none}.drawer-header{flex-shrink:0;display:flex;justify-content:space-between;align-items:flex-start;gap:16px;padding:18px 22px;border-bottom:1px solid var(--border)}.drawer-header h3{margin:0}.drawer-header p{margin:6px 0 0}.drawer-body{flex:1;min-height:0;overflow-y:auto;overscroll-behavior:contain;padding:20px 22px}.request-id{overflow-wrap:anywhere}.request-detail h3{margin:24px 0 12px}.request-facts{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin:20px 0}.request-facts div{min-width:0}.request-facts dt{font-size:12px;color:var(--text-muted)}.request-facts dd{margin:4px 0 0;overflow-wrap:anywhere}.route-row{padding:12px 0;border-bottom:1px solid var(--border)}.route-row p{margin:6px 0}.check{display:flex;align-items:center;gap:8px;margin:12px 0}.check input{width:auto}.scroll{overflow-x:auto}table{width:100%;border-collapse:collapse;text-align:left;font-size:12px}th,td{padding:9px 8px;border-bottom:1px solid var(--border);white-space:nowrap}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:var(--surface-sunken);border:1px solid var(--border);padding:12px;border-radius:7px;max-height:400px;overflow:auto}.content-section{padding-bottom:24px}.skeleton-line{height:20px;margin:18px 0;background:var(--surface-sunken,#eee);border-radius:5px}.skeleton-line:nth-child(even){width:75%}button:disabled{opacity:.5;cursor:default}@media(max-width:720px){.request-drawer{width:100%;min-width:0;border:0}.drawer-header{padding:14px 16px}.drawer-body{padding:16px}.request-facts{grid-template-columns:1fr}.drawer-header button{flex-shrink:0}}
</style>
