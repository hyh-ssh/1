<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from './api'
import type { PageContext } from './navigation'
const props = defineProps<{context?: PageContext}>()
const emit = defineEmits<{(e:'context-change', value:PageContext):void}>()
const data = ref<Record<string, any>>({}), runtime = ref<Record<string, any>>({}), providers = ref<any[]>([])
const error = ref(''), busy = ref(false), loaded = ref(false), ready = ref<boolean | null>(null)
const period = ref(24), apiType = ref(''), user = ref(''), provider = ref(''), modelGroup = ref('')
let generation = 0
function restoreFilters() {
  period.value = [24,168,720].includes(Number(props.context?.period)) ? Number(props.context?.period) : 24
  apiType.value = props.context?.api || ''; user.value = props.context?.user_id || ''
  provider.value = props.context?.provider_id || ''; modelGroup.value = props.context?.model_group_id || ''
}
async function submitFilters() {
  emit('context-change', {period:String(period.value),api:apiType.value,user_id:user.value,provider_id:provider.value,model_group_id:modelGroup.value})
  await load()
}
async function load() {
  const current = ++generation
  busy.value = true; error.value = ''
  const q = new URLSearchParams({ from: new Date(Date.now() - period.value * 3600000).toISOString() })
  if (apiType.value) q.set('api', apiType.value)
  if (user.value) q.set('user_id', user.value)
  if (provider.value) q.set('provider_id', provider.value)
  if (modelGroup.value) q.set('model_group_id', modelGroup.value)
  try {
    const [stats, health, accounts, status] = await Promise.all([api.resource(`admin/stats?${q}`), api.resource('admin/runtime'), api.resource('admin/providers'), fetch('/readyz').then(r => r.ok)])
    if (current !== generation) return
    if (stats.error) error.value = stats.error; else { data.value = stats.data || {}; loaded.value = true }
    runtime.value = health.data || {}; providers.value = accounts.data || []; ready.value = status
    if (health.error || accounts.error) error.value ||= health.error || accounts.error || ''
  } catch { if (current === generation) { error.value = '无法读取服务状态'; ready.value = false } }
  finally { if (current === generation) busy.value = false }
}
const cards = computed(() => [ ['请求数', data.value.requests ?? '—'], ['成功率', `${Number(data.value.success_rate || 0).toFixed(1)}%`], ['上游成本', `¥${data.value.cost || 0}`], ['积分消耗', `${data.value.credits || 0}`], ['Token', data.value.total_tokens || 0], ['P50 / P95 / P99 延迟', `${Math.round(data.value.p50_latency_ms || 0)} / ${Math.round(data.value.p95_latency_ms || 0)} / ${Math.round(data.value.p99_latency_ms || 0)} ms`], ['P95 首字延迟', `${Math.round(data.value.p95_ttft_ms || 0)} ms`], ['故障转移 / 结算异常', `${data.value.failover_count || 0} / ${data.value.settlement_errors || 0}`] ])
const trend = computed(() => {
  const byHour = new Map((data.value.trend || []).map((p: any) => [Date.parse(p.period), p]))
  const now = Math.floor(Date.now() / 3600000) * 3600000
  return Array.from({ length: period.value }, (_, i) => byHour.get(now - (period.value - 1 - i) * 3600000) || { period: new Date(now - (period.value - 1 - i) * 3600000).toISOString(), requests: 0, success: 0 }) as any[]
})
const maxRequests = computed(() => Math.max(1, ...trend.value.map(p => p.requests)))
const points = (key: string) => trend.value.map((p, i) => `${20 + i * 760 / Math.max(1, trend.value.length - 1)},${180 - 150 * Number(p[key] || 0) / maxRequests.value}`).join(' ')
const failedWrites = computed(() => Object.values(runtime.value).reduce((sum: number, item: any) => sum + Number(item.failed || 0) + Number(item.rejected || 0), 0))
watch(() => props.context, () => { restoreFilters(); void load() })
onMounted(() => { restoreFilters(); void load() })
onUnmounted(() => { generation++ })
</script>

<template>
  <div><form class="panel dashboard-filters" @submit.prevent="submitFilters"><label>时间范围<select v-model.number="period"><option :value="24">最近 24 小时</option><option :value="168">最近 7 天</option><option :value="720">最近 30 天</option></select></label><label>协议<select v-model="apiType"><option value="">全部协议</option><option v-for="kind in ['chat_completions','responses','messages','embeddings','images_generations']" :key="kind">{{ kind }}</option></select></label><label>用户 ID<input v-model="user" type="number" min="1" /></label><label>供应商<select v-model="provider"><option value="">全部</option><option v-for="p in providers" :value="p.id" :key="p.id">{{ p.name }}</option></select></label><label>模型组 ID<input v-model="modelGroup" type="number" min="1" /></label><button class="secondary" :disabled="busy">{{ busy ? '加载中…' : '刷新统计' }}</button></form>
  <p v-if="error" class="alert" role="alert">{{ error }} <button type="button" @click="load">重试读取</button></p><p v-if="busy" role="status" class="muted">正在加载统计…</p><div v-if="loaded" class="cards"><article v-for="[label, value] in cards" :key="label" class="card"><span class="card-label">{{ label }}</span><strong>{{ value }}</strong></article></div>
  <div class="grid-two"><article class="panel"><h3>每小时请求趋势</h3><p class="muted">蓝色：全部请求 · 绿色：成功请求 · 峰值 {{ maxRequests }}</p><svg viewBox="0 0 800 210" role="img" aria-label="每小时全部请求和成功请求趋势"><path d="M20 20V180H780" stroke="#c8d3e4" fill="none" /><polyline :points="points('requests')" fill="none" stroke="#3563e9" stroke-width="3" /><polyline :points="points('success')" fill="none" stroke="#12a06a" stroke-width="2" /></svg><p class="muted">{{ new Date(trend[0].period).toLocaleString() }} — {{ new Date(trend[trend.length - 1].period).toLocaleString() }}</p></article><article class="panel"><h3>运行状态</h3><ul class="status-list"><li>数据库就绪 <b>{{ ready === null ? '读取中' : ready ? '正常' : '不可用' }}</b></li><li>启用的供应商 <b>{{ providers.filter(p => p.active).length }}</b></li><li>冷却中的供应商 <b>{{ providers.filter(p => p.cooldown_until && Date.parse(p.cooldown_until) > Date.now()).length }}</b></li><li>待持久化任务 <b>{{ (runtime.records?.pending || 0) + (runtime.billing?.pending || 0) }}</b></li><li>持久化失败 / 拒绝 <b :class="{ error: failedWrites > 0 }">{{ failedWrites }}</b></li></ul><p class="muted">持久化计数从本次进程启动开始；发生失败时应核对账本与运行日志。</p></article></div>
  <article v-if="loaded" class="panel routes"><h3>路由表现</h3><p class="muted">失败尝试成本 ¥{{ data.failed_attempt_cost || 0 }} · 估算用量请求 {{ data.estimated_count || 0 }}</p><div class="scroll"><table><thead><tr><th>模型 / 上游</th><th>供应商</th><th>尝试数</th><th>成功率</th><th>P95</th><th>成本</th><th>积分</th><th>结算异常</th></tr></thead><tbody><tr v-for="r in data.routes" :key="`${r.model_alias}-${r.provider_id}-${r.provider_model}`"><td>{{ r.model_alias }} / {{ r.provider_model }}</td><td>{{ providers.find(p => p.id === r.provider_id)?.name || r.provider_id }}</td><td>{{ r.attempts }}</td><td>{{ Number(r.success_rate).toFixed(1) }}%</td><td>{{ Math.round(r.p95_latency_ms) }} ms</td><td>¥{{ r.cost }}</td><td>{{ r.credits }}</td><td>{{ r.settlement_errors }}</td></tr></tbody></table><p v-if="!busy && !data.routes?.length" class="empty">所选时间范围暂无路由数据</p></div></article>
  </div>
</template>
<style scoped>.dashboard-filters{display:flex;align-items:end;gap:12px;flex-wrap:wrap;margin-bottom:20px}.dashboard-filters label{flex:1;min-width:120px}.card strong{font-size:19px;overflow-wrap:anywhere}svg{width:100%;height:auto;margin-top:15px}.routes{margin-top:20px}.scroll{overflow:auto}table{width:100%;font-size:12px;border-collapse:collapse;text-align:left;margin-top:15px}th,td{padding:12px;border-bottom:1px solid var(--border);white-space:nowrap}button:disabled{opacity:.5}</style>
