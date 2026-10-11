<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from './api'
import { localizeError } from './errors'
import OperationsRequestDrawer from './OperationsRequestDrawer.vue'

type Row = Record<string, any>
type Context = Record<string, string>
const props = defineProps<{ view: string; context?: Context }>()
const emit = defineEmits<{
  'context-change': [context: Context]
  navigate: [target: { view: string; context?: Context }]
}>()
const rows = ref<Row[]>([]), providers = ref<Row[]>([]), aliases = ref<Row[]>([])
const selected = ref<Row | null>(null), edit = ref<Row | null>(null)
const routes = ref<Row[]>([]), models = ref<Row[]>([]), routeModels = ref<Row[]>([])
const error = ref(''), notice = ref(''), busy = ref(false), loading = ref(false)
const configLoading = ref(false), configError = ref(''), routeModelLoading = ref(false), routeModelError = ref('')
const requestId = ref(''), configId = ref('')
const requestKeys = ['from', 'to', 'request_id', 'user_id', 'api_key_id', 'model', 'api', 'provider_id', 'status', 'settlement_status', 'model_group_id']
const auditKeys = ['from', 'to', 'actor_id', 'resource', 'action', 'result']
const filters = ref<Context>({}), appliedFilters = ref<Context>({})
const page = ref(1)
const newRoute = ref({ provider_id: 0, provider_model: '', priority: 10 })
const modelMode = ref<'select' | 'manual'>('select'), manualModel = ref('')
const capabilityKeys = ['streaming', 'multimodal', 'tool_calling', 'structured_output', 'reasoning', 'responses_state', 'embeddings', 'images_generations'] as const
const capabilityLabels: Record<string, string> = { streaming: '流式', multimodal: '多模态', tool_calling: '工具调用', structured_output: '结构化输出', reasoning: '推理', responses_state: 'Responses 状态', embeddings: 'Embeddings', images_generations: '图像生成' }
const blankCaps = (): Record<string, boolean> => ({ streaming: false, multimodal: false, tool_calling: false, structured_output: false, reasoning: false, responses_state: false, embeddings: false, images_generations: false })
const newModelCaps = ref(blankCaps())
function withCaps(list: Row[]): Row[] { return list.map(item => ({ ...item, capabilities: { ...blankCaps(), ...(item.capabilities || {}) } })) }
const dragged = ref<number | null>(null)
const readiness = ref<Record<string, Row>>({}), readinessErrors = ref<Record<string, string>>({}), readinessLoading = ref<Record<string, boolean>>({})
const titles: Record<string, string> = { providers: '供应商与模型', routing: '别名与路由', modelGroups: '模型组', requests: '请求记录', audit: '审计日志' }
const endpoint = computed(() => ({ providers: 'providers', routing: 'aliases', modelGroups: 'model-groups', requests: 'requests', audit: 'audit' } as Record<string, string>)[props.view])
const paginated = computed(() => props.view === 'requests' || props.view === 'audit')
const filterKeys = computed(() => props.view === 'requests' ? requestKeys : props.view === 'audit' ? auditKeys : [])
let generation = 0, lifecycle = 0, selectionGeneration = 0, modelGeneration = 0, alive = true
let listAbort: AbortController | null = null, selectionAbort: AbortController | null = null, modelAbort: AbortController | null = null

function resultError(result: any): string {
  return [localizeError(String(result.error)), result.status ? `HTTP ${result.status}` : '', result.code ? `错误码：${result.code}` : '', result.request_id ? `诊断请求 ID：${result.request_id}` : ''].filter(Boolean).join(' · ')
}
async function get(name: string, signal?: AbortSignal): Promise<any> {
  const result = await api.resource(`admin/${name}`, signal ? { signal } : undefined)
  if (result.error) throw new Error(resultError(result))
  return result.data
}
const current = (token: number) => alive && lifecycle === token
async function mutate(name: string, method: string, payload: Row | undefined, token: number) {
  if (!current(token)) throw new Error('操作上下文已改变，请重新确认。')
  const result = await api.resource(`admin/${name}`, { method, body: payload ? JSON.stringify(payload) : undefined })
  if (result.error) throw new Error(resultError(result))
  return result.data
}
async function action(fn: (token: number) => Promise<void>) {
  if (busy.value) return
  const token = lifecycle
  busy.value = true; error.value = ''; notice.value = ''
  try { await fn(token) }
  catch (e) { if (current(token)) error.value = e instanceof Error ? e.message : '操作失败' }
  finally { if (current(token)) busy.value = false }
}
function safeContext(source: Context | undefined): Context {
  const context: Context = {}
  const number = Number(source?.page || 1)
  if (paginated.value) context.page = String(Number.isSafeInteger(number) && number > 0 && number <= 1000000 ? number : 1)
  for (const key of filterKeys.value) if (typeof source?.[key] === 'string' && source[key]!.trim()) context[key] = source[key]!.trim()
  if (['requests', 'providers', 'routing'].includes(props.view) && typeof source?.detail === 'string' && source.detail.trim()) context.detail = source.detail.trim()
  return context
}
function contextSnapshot(): Context {
  const context = safeContext({ ...appliedFilters.value, page: String(page.value) })
  const detail = props.view === 'requests' ? requestId.value : configId.value
  if (detail) context.detail = detail
  return context
}
function publishContext() { emit('context-change', contextSnapshot()) }
function listSignature(context: Context): string {
  return JSON.stringify([context.page || '1', ...filterKeys.value.map(key => context[key] || '')])
}
function restoreFilters(context: Context) {
  const restored: Context = {}
  for (const key of filterKeys.value) restored[key] = context[key] || ''
  filters.value = { ...restored }; appliedFilters.value = restored
  page.value = Number(context.page || 1)
}
function utcFilter(value: string): string {
  if (!value) return ''
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d{1,9})?)?(?:Z|[+-]\d{2}:\d{2})?$/i.test(value)) throw new Error('时间格式无效，请使用 UTC 日期时间，如 2026-10-03T00:00:00Z。')
  const calendar = new Date(`${value.slice(0, 10)}T00:00:00Z`)
  if (!Number.isFinite(calendar.getTime()) || calendar.toISOString().slice(0, 10) !== value.slice(0, 10) || Number(value.slice(11, 13)) > 23 || Number(value.slice(14, 16)) > 59 || (value[16] === ':' && Number(value.slice(17, 19)) > 59)) throw new Error('UTC 日期时间无效，请检查日期与时分秒。')
  const explicitZone = /(?:Z|[+-]\d{2}:\d{2})$/i.test(value)
  const date = new Date(explicitZone ? value : `${value}Z`)
  if (!Number.isFinite(date.getTime())) throw new Error('时间格式无效，请使用 UTC 日期时间。')
  const fraction = value.match(/\.(\d{1,9})/)?.[1]
  return fraction && fraction.length > 3 ? date.toISOString().replace(/\.\d{3}Z$/, `.${fraction}Z`) : date.toISOString()
}
function validatedFilters(source: Context): Context {
  const normalized: Context = {}
  for (const key of filterKeys.value) {
    const value = String(source[key] ?? '').trim()
    if (!value) continue
    if (value.length > 200 || /[\u0000-\u001f]/.test(value)) throw new Error('筛选值无效或过长，请检查链接或筛选条件。')
    if (['user_id', 'api_key_id', 'provider_id', 'model_group_id', 'actor_id'].includes(key) && !/^[1-9]\d*$/.test(value)) throw new Error('用户、密钥、供应商及模型组 ID 必须为正整数。')
    normalized[key] = key === 'from' || key === 'to' ? utcFilter(value) : value
  }
  if (normalized.from && normalized.to) {
    // UTC RFC3339 fractions are compared at nanosecond precision, without rounding via Date.
    const comparable = (value: string) => value.replace(/\.(\d+)Z$/, (_, fraction: string) => `.${fraction.padEnd(9, '0')}Z`)
    if (comparable(normalized.from) > comparable(normalized.to)) throw new Error('开始时间不得晚于结束时间（UTC）。')
  }
  return normalized
}
async function submitFilters() {
  try {
    const normalized = validatedFilters(filters.value)
    appliedFilters.value = normalized; filters.value = { ...normalized }; page.value = 1
    requestId.value = ''; publishContext(); await load()
  } catch (e) { error.value = e instanceof Error ? e.message : '筛选条件无效' }
}
async function changePage(delta: number) {
  if (loading.value || page.value + delta < 1) return
  page.value += delta; requestId.value = ''; publishContext(); await load()
}
function resetFilters() { filters.value = {}; void submitFilters() }
async function refreshReadiness(row: Row, expectedGeneration = generation) {
  const id = String(row.id)
  readinessLoading.value[id] = true; delete readinessErrors.value[id]
  try {
    const data = await get(`aliases/${encodeURIComponent(id)}/readiness`, listAbort?.signal)
    if (alive && generation === expectedGeneration && props.view === 'routing') readiness.value[id] = data
  } catch (e) { if (alive && generation === expectedGeneration) readinessErrors.value[id] = e instanceof Error ? e.message : '就绪状态读取失败' }
  finally { if (alive && generation === expectedGeneration) readinessLoading.value[id] = false }
}
async function load() {
  const expected = ++generation, view = props.view
  listAbort?.abort(); listAbort = new AbortController()
  if (!endpoint.value) { rows.value = []; loading.value = false; return }
  loading.value = true; error.value = ''
  try {
    const query = new URLSearchParams()
    if (paginated.value) {
      for (const [key, value] of Object.entries(validatedFilters(appliedFilters.value))) query.set(key, value)
      query.set('offset', String((page.value - 1) * 50)); query.set('limit', '50')
    }
    const data = await get(`${endpoint.value}${query.size ? `?${query}` : ''}`, listAbort.signal)
    if (!alive || expected !== generation || view !== props.view) return
    rows.value = Array.isArray(data) ? data : []
    if (view === 'routing') for (const row of rows.value) {
      if (row.readiness) readiness.value[String(row.id)] = row.readiness
      else void refreshReadiness(row, expected)
    }
    if (configId.value && ['routing', 'providers'].includes(view)) {
      const row = rows.value.find(item => String(item.id) === configId.value)
      if (row) { selected.value = row; await loadConfig(row) }
      else { selected.value = { id: configId.value }; configError.value = '配置链接已失效，目标不存在或不可访问。'; configLoading.value = false }
    }
  } catch (e) { if (alive && expected === generation) error.value = e instanceof Error ? e.message : '加载失败' }
  finally { if (alive && expected === generation) loading.value = false }
}
function clearConfig() {
  ++selectionGeneration; ++modelGeneration; selectionAbort?.abort(); modelAbort?.abort()
  selected.value = null; configId.value = ''; routes.value = []; models.value = []; routeModels.value = []
  configLoading.value = false; configError.value = ''; routeModelLoading.value = false; routeModelError.value = ''
  newRoute.value = { provider_id: 0, provider_model: '', priority: 10 }
  manualModel.value = ''; newModelCaps.value = blankCaps()
}
let previousView = ''
watch([() => props.view, () => props.context], () => {
  const changedView = previousView !== props.view, incoming = safeContext(props.context)
  if (changedView) {
    ++lifecycle; busy.value = false; rows.value = []; edit.value = null; requestId.value = ''; clearConfig()
    readiness.value = {}; readinessErrors.value = {}; readinessLoading.value = {}; error.value = ''; notice.value = ''
    previousView = props.view
  }
  const changedList = changedView || listSignature(incoming) !== listSignature(contextSnapshot())
  if (changedList) restoreFilters(incoming)
  if (props.view === 'requests') requestId.value = incoming.detail || ''
  else if (['providers', 'routing'].includes(props.view)) {
    const id = incoming.detail || ''
    if (configId.value !== id) {
      clearConfig(); configId.value = id
      if (!changedList && id) {
        const row = rows.value.find(item => String(item.id) === id)
        if (row) { selected.value = row; void loadConfig(row) }
        else { selected.value = { id }; configError.value = '配置链接已失效，目标不存在或不可访问。' }
      }
    }
  }
  if (changedList) void load()
}, { immediate: true, deep: true })
onBeforeUnmount(() => {
  alive = false; ++lifecycle; ++generation; listAbort?.abort(); clearConfig(); edit.value = null; rows.value = []; requestId.value = ''
})

async function confirmImpact(resource: string, name: string, verb: string, token: number): Promise<boolean> {
  const impact = await get(`${resource}/impact`)
  if (!current(token)) return false
  if (!impact || !Array.isArray(impact.affected)) throw new Error('无法取得服务器影响清单；未执行变更，请重试。')
  const affected: Row[] = impact.affected
  const lines = affected.map(item => `「${item.alias_name}」（ID ${item.alias_id}，${item.active ? '启用' : '停用'}）：剩余有效路由 ${item.remaining_routes} 条；${item.loses_last_route ? '将失去最后有效路由' : item.remaining_routes > 0 ? '仍有替代路由' : '原无有效路由，未失去最后路由'}`)
  const message = [`确认${verb}「${name}」？`, `服务器影响清单：${affected.length} 个别名；${affected.filter(item => item.loses_last_route).length} 个将失去最后有效路由。`, ...lines, impact.high_risk ? '高风险变更：可能中断以上模型调用。' : '', '只变更当前对象，不会自动修改其他对象的启停状态。'].filter(Boolean).join('\n')
  return window.confirm(message)
}
async function requireEffectiveRoute(row: Row, token: number) {
  const data = await get(`aliases/${row.id}/readiness`)
  if (!current(token)) return
  readiness.value[String(row.id)] = data
  if (!data || Number(data.effective_routes) < 1 || !Number.isFinite(Number(data.effective_routes))) throw new Error(`无法启用「${row.name}」：至少需要一条启用路由指向存在且启用的供应商。${data?.reasons?.map(readinessReason).join('；') || '请先配置有效路由。'} 定价未配置只提示，免费配置合法。`)
}
async function openEditor(row?: Row) {
  await action(async token => {
    if (props.view === 'modelGroups') {
      const data = await get('aliases')
      if (!current(token)) return
      aliases.value = data || []
    }
    if (!current(token)) return
    edit.value = row ? { ...JSON.parse(JSON.stringify(row)), compatibility_profile: row.compatibility_profile || 'generic', alias_ids: [...(row.alias_ids || [])] } : { name: '', active: props.view !== 'routing', type: 'openai_compatible', compatibility_profile: 'generic', base_url: '', timeout_ms: 30000, max_concurrency: 0, supports_responses: false, alias_ids: [] }
    if (edit.value) edit.value.api_key = ''
  })
}
async function save() {
  const edited = edit.value, view = props.view, resource = endpoint.value
  if (!edited) return
  await action(async token => {
    if (!edited.name?.trim()) throw new Error('名称不能为空')
    let body: Row = { name: edited.name.trim() }
    if (view === 'providers') {
      if (!['generic', 'deepseek'].includes(edited.compatibility_profile)) throw new Error('兼容配置只能选择通用或 DeepSeek')
      if (edited.compatibility_profile === 'deepseek' && edited.type !== 'openai_compatible') throw new Error('DeepSeek 兼容配置仅支持 OpenAI 兼容协议')
      body = { ...body, type: edited.type, compatibility_profile: edited.compatibility_profile, base_url: edited.base_url, active: edited.active, supports_responses: !!edited.supports_responses, timeout_ms: Number(edited.timeout_ms), max_concurrency: Number(edited.max_concurrency) }
      if (edited.api_key) body.api_key = edited.api_key
    }
    if (view === 'routing') body.active = edited.id ? !!edited.active : false
    if (view === 'modelGroups') body = { ...body, active: !!edited.active, alias_ids: edited.alias_ids }
    const original = rows.value.find(row => row.id === edited.id)
    if (view === 'routing' && edited.id && !original?.active && body.active) await requireEffectiveRoute(edited, token)
    if (edited.id && ['routing', 'providers'].includes(view) && original?.active && !body.active && !await confirmImpact(`${resource}/${edited.id}`, original.name, '停用', token)) return
    await mutate(`${resource}${edited.id ? `/${edited.id}` : ''}`, edited.id ? 'PATCH' : 'POST', body, token)
    if (!current(token)) return
    edit.value = null; await load(); if (current(token)) notice.value = view === 'routing' && !edited.id ? '草稿已保存。配置有效路由后可启用。' : '已保存'
  })
}
async function toggle(row: Row) {
  const resource = endpoint.value, view = props.view
  await action(async token => {
    if (view === 'routing' && !row.active) await requireEffectiveRoute(row, token)
    if (row.active && ['providers', 'routing'].includes(view) && !await confirmImpact(`${resource}/${row.id}`, row.name, '停用', token)) return
    await mutate(`${resource}/${row.id}`, 'PATCH', { active: !row.active }, token)
    if (current(token)) { await load(); if (current(token)) notice.value = '状态已更新' }
  })
}
async function probe(row: Row, operation: 'verify' | 'discover') {
  await action(async token => {
    const data = await mutate(`providers/${row.id}/${operation}`, 'POST', {}, token)
    if (!current(token)) return
    await load(); if (!current(token)) return
    notice.value = `${operation === 'verify' ? '连接验证通过' : '模型发现完成'}，发现 ${data.models?.length || 0} 个模型`
  })
}
async function loadConfig(row: Row) {
  const expected = ++selectionGeneration, view = props.view
  selectionAbort?.abort(); selectionAbort = new AbortController()
  configLoading.value = true; configError.value = ''; models.value = []; routes.value = []
  try {
    if (view === 'providers') {
      const data = await get(`providers/${row.id}/models`, selectionAbort.signal)
      if (alive && expected === selectionGeneration) models.value = withCaps(data || [])
    }
    if (view === 'routing') {
      const [routeData, providerData, readyData] = await Promise.all([get(`aliases/${row.id}/routes`, selectionAbort.signal), get('providers', selectionAbort.signal), get(`aliases/${row.id}/readiness`, selectionAbort.signal)])
      if (!alive || expected !== selectionGeneration) return
      routes.value = routeData || []; providers.value = providerData || []; readiness.value[String(row.id)] = readyData
      modelMode.value = 'select'; newRoute.value = { provider_id: providers.value.find(provider => provider.active)?.id || providers.value[0]?.id || 0, provider_model: '', priority: (routes.value.length + 1) * 10 }
    }
  } catch (e) { if (alive && expected === selectionGeneration) configError.value = e instanceof Error ? e.message : '配置读取失败' }
  finally { if (alive && expected === selectionGeneration) configLoading.value = false }
}
function openRow(row: Row, event?: Event) {
  if (event?.currentTarget instanceof HTMLElement) event.currentTarget.focus({ preventScroll: true })
  if (props.view === 'requests') { requestId.value = String(row.id); publishContext(); return }
  selected.value = row; configId.value = String(row.id); publishContext(); void loadConfig(row)
}
function closeConfig() { clearConfig(); publishContext() }
function closeRequest() { requestId.value = ''; publishContext() }
watch(() => newRoute.value.provider_id, async id => {
  const expected = ++modelGeneration
  modelAbort?.abort(); modelAbort = new AbortController()
  newRoute.value.provider_model = ''; routeModels.value = []; routeModelError.value = ''
  if (!id || props.view !== 'routing' || !selected.value) { routeModelLoading.value = false; return }
  routeModelLoading.value = true
  try {
    const data = await get(`providers/${id}/models`, modelAbort.signal)
    if (alive && expected === modelGeneration) routeModels.value = data || []
  } catch (e) { if (alive && expected === modelGeneration) routeModelError.value = e instanceof Error ? e.message : '模型目录读取失败，可选择显式手工填写。' }
  finally { if (alive && expected === modelGeneration) routeModelLoading.value = false }
})
async function addModel() {
  const id = selected.value?.id
  await action(async token => {
    if (!id || !manualModel.value.trim()) throw new Error('模型 ID 不能为空')
    await mutate(`providers/${id}/models`, 'POST', { model_id: manualModel.value.trim(), capabilities: { ...newModelCaps.value } }, token)
    if (!current(token)) return
    const data = await get(`providers/${id}/models`)
    if (!current(token) || selected.value?.id !== id) return
    models.value = withCaps(data || []); manualModel.value = ''; newModelCaps.value = blankCaps(); notice.value = '模型已添加'
  })
}
async function saveModelCaps(model: Row) {
  const id = selected.value?.id
  await action(async token => {
    await mutate(`providers/${id}/models`, 'POST', { model_id: model.model_id, capabilities: { ...model.capabilities } }, token)
    if (!current(token)) return
    const data = await get(`providers/${id}/models`)
    if (!current(token) || selected.value?.id !== id) return
    models.value = withCaps(data || []); notice.value = '模型能力已保存'
  })
}
async function reloadRoutes(id: number, token: number) {
  const [routeData, readyData] = await Promise.all([get(`aliases/${id}/routes`), get(`aliases/${id}/readiness`)])
  if (!current(token) || selected.value?.id !== id) return
  routes.value = routeData || []; readiness.value[String(id)] = readyData
}
async function addRoute() {
  const id = selected.value?.id, body = { ...newRoute.value, provider_model: newRoute.value.provider_model.trim() }
  await action(async token => {
    if (!body.provider_id || !body.provider_model) throw new Error('请选择供应商，并从目录选择模型或显式手工填写真实模型名')
    if (modelMode.value === 'select' && !routeModels.value.some(model => model.model_id === body.provider_model)) throw new Error('请从供应商模型目录选择；自定义模型请切换为手工填写。')
    await mutate(`aliases/${id}/routes`, 'POST', body, token)
    if (!current(token)) return
    await reloadRoutes(id, token)
    if (current(token) && selected.value?.id === id) { newRoute.value.provider_model = ''; notice.value = '路由已添加；模型启停状态未自动改变。' }
  })
}
async function move(from: number, to: number) {
  if (to < 0 || to >= routes.value.length || from === to) return
  const id = selected.value?.id
  await action(async token => {
    const reordered = [...routes.value], [item] = reordered.splice(from, 1); reordered.splice(to, 0, item!)
    await mutate(`aliases/${id}/routes/order`, 'PATCH', { route_ids: reordered.map(route => route.id) }, token)
    if (current(token)) await reloadRoutes(id, token)
  })
}
function drop(to: number) { if (dragged.value !== null) void move(dragged.value, to); dragged.value = null }
async function routeActive(row: Row) {
  const id = selected.value?.id, resource = `aliases/${id}/routes/${row.id}`
  await action(async token => {
    if (row.active && !await confirmImpact(resource, row.provider_model, '停用路由', token)) return
    await mutate(resource, 'PATCH', { active: !row.active }, token)
    if (current(token)) await reloadRoutes(id, token)
  })
}
async function removeResource(row: Row) {
  const resource = `${endpoint.value}/${row.id}`, name = props.view === 'routing' ? '删除别名及其路由' : '删除供应商及其目录与路由'
  await action(async token => {
    if (!await confirmImpact(resource, row.name, name, token)) return
    await mutate(resource, 'DELETE', undefined, token)
    if (!current(token)) return
    if (selected.value?.id === row.id) closeConfig()
    await load(); if (current(token)) notice.value = '已删除当前对象，其他对象状态未自动改变。'
  })
}
async function removeRoute(row: Row) {
  const id = selected.value?.id, resource = `aliases/${id}/routes/${row.id}`
  await action(async token => {
    if (!await confirmImpact(resource, row.provider_model, '移除路由', token)) return
    await mutate(resource, 'DELETE', undefined, token)
    if (current(token)) await reloadRoutes(id, token)
  })
}
const time = (value: string) => value && Number.isFinite(Date.parse(value)) ? `${new Date(value).toISOString().replace('T', ' ').replace('Z', '')} UTC` : '—'
const health = (row: Row) => !row.active ? '已停用' : row.cooldown_until && Date.parse(row.cooldown_until) > Date.now() ? '冷却中' : row.health_status === 'cooldown' ? '等待恢复探测' : '可用'
function statusLabel(value: string): string { return ({ success: '成功', error: '失败', failed: '失败', pending: '处理中', cancelled: '已取消' } as Context)[value] || (value ? `未知（${value}）` : '未记录') }
function settlementLabel(value: string): string { return ({ settled: '已结算', free: '免费（显式零费率）', credits_free: '免积分（成本独立计量）', unpriced: '未定价（非免费）', failed: '结算失败', none: '无需结算' } as Context)[value] || (value ? `未知（${value}）` : '未记录（历史记录）') }
function settlementHint(value: string): string {
  if (value === 'unpriced') return '未配置价格版本，本次未计费；若要表达免费，请在「价格」中创建成本和积分均为 0 的零费率版本'
  return `结算异常（${value}），需核对账本与价格配置`
}
function pricingLabel(value: string): string { return ({ priced: '已定价', free: '免费（显式零费率）', credits_free: '免积分（成本独立计量）', unpriced: '未定价（只提示，不阻止调用）', mixed: '混合定价（请检查各路由）' } as Context)[value] || '定价未知' }
function protocolsLabel(value: any): string { return Array.isArray(value) && value.length ? value.join('、') : '未知（无可确认协议，不能推断为支持）' }
function readinessReason(value: string): string {
  return ({ route_inactive: '存在停用路由，请检查路由配置。', provider_missing: '路由供应商不存在，请选择有效供应商。', provider_inactive: '路由供应商已停用，请检查供应商配置。', pricing_missing: '缺少价格版本；未定价不等同免费，不阻止调用。', alias_inactive: '模型尚未启用（草稿），配置有效路由后可启用。', no_effective_route: '没有指向启用供应商的启用路由。', capabilities_unknown: '模型能力未知，请在供应商模型目录确认。' } as Context)[value] || localizeError(value)
}
function readinessProviders(row: Row): string {
  const values = readyFor(row)?.routes?.map((route: Row) => `${route.provider_name || `供应商 ${route.provider_id}`}（${!route.provider_name ? '不存在' : route.provider_active ? '启用' : '停用'}）`) || []
  return values.length ? [...new Set<string>(values)].join('、') : '尚未配置供应商路由'
}
const readyFor = (row: Row): Row | undefined => readiness.value[String(row.id)]
const routeReady = (route: Row): Row | undefined => selected.value ? readyFor(selected.value)?.routes?.find((item: Row) => item.id === route.id) : undefined
function actorLabel(row: Row): string { return row.actor_name || (row.actor_id ? `用户 ${row.actor_id}` : '未记录操作者') }
function auditResult(value: string): string { return ({ success: '成功', failure: '失败', denied: '拒绝' } as Context)[value] || '未知（历史记录未记录结果）' }
// Never render legacy detail or arbitrary objects: only business-safe summary fields.
const safeAuditKeys = new Set(['id', 'name', 'username', 'active', 'role', 'user_id', 'owner_id', 'api_key_id', 'provider_id', 'alias_id', 'model_id', 'provider_model', 'model_allow', 'alias_ids', 'type', 'compatibility_profile', 'supports_responses', 'timeout_ms', 'max_concurrency', 'scope', 'scope_id', 'rpm', 'tpm', 'concurrency', 'expires_at', 'version', 'price_version', 'priority', 'route_ids', 'enabled', 'password_login_enabled', 'local_enabled'])
function safeAuditSummary(value: unknown, result?: string): string {
  if (!value) return ['success', 'failure', 'denied'].includes(result || '') ? '无摘要（对象不存在或未记录）' : '未记录（历史记录无前后摘要）'
  let source: any = value
  if (typeof source === 'string') { try { source = JSON.parse(source) } catch { return '无可展示的安全摘要' } }
  if (!source || typeof source !== 'object' || Array.isArray(source)) return '无可展示的安全摘要'
  const safe: Row = {}
  for (const [key, field] of Object.entries(source)) if (safeAuditKeys.has(key)) {
    if (field === null || typeof field === 'boolean' || typeof field === 'number' || typeof field === 'string') safe[key] = field
    else if (Array.isArray(field) && ['model_allow', 'alias_ids', 'route_ids'].includes(key)) safe[key] = field.filter(item => typeof item === 'string' || typeof item === 'number')
  }
  return Object.keys(safe).length ? JSON.stringify(safe, null, 2) : '无可展示的安全字段'
}
</script>

<template>
  <div class="ops">
    <template v-if="endpoint">
      <div class="toolbar"><div><h2>{{ titles[view] }}</h2><p class="muted">{{ view === 'modelGroups' ? '模型组仅用于分类、别名关联及统计/请求筛选；模型访问由密钥白名单决定。' : view === 'routing' ? '按优先级调用有效上游。模型先保存草稿，配置有效路由后启用；输出开始后不再切换。' : view === 'requests' || view === 'audit' ? '按服务器实际记录检索；所有时间统一显示为 UTC。' : '查看配置、健康状态与模型目录。' }}</p></div><div class="toolbar-actions"><button v-if="!paginated" class="primary compact" :disabled="busy" @click="openEditor()">+ 新建</button><button class="secondary" :disabled="busy || loading" @click="load">刷新</button></div></div>
      <p v-if="error" class="alert" role="alert">{{ error }}</p><p v-if="notice" class="notice" role="status">{{ notice }}</p>
      <form v-if="paginated" class="panel form-grid filters" @submit.prevent="submitFilters">
        <p class="muted wide">起止时间含边界，输入与显示均为 UTC（例如 2026-10-03T00:00:00Z）；不会按浏览器本地时区解释。</p>
        <label>开始时间（UTC）<input v-model="filters.from" name="from" placeholder="2026-10-03T00:00:00Z" /></label><label>结束时间（UTC）<input v-model="filters.to" name="to" placeholder="2026-10-03T23:59:59Z" /></label>
        <template v-if="view === 'requests'">
          <label>请求 ID（精确匹配）<input v-model="filters.request_id" name="request_id" maxlength="200" /></label><label>用户 ID<input v-model="filters.user_id" name="user_id" type="number" min="1" /></label><label>密钥 ID<input v-model="filters.api_key_id" name="api_key_id" type="number" min="1" /></label><label>模型别名<input v-model="filters.model" name="model" maxlength="200" /></label><label>协议<select v-model="filters.api" name="api"><option value="">全部</option><option v-for="kind in ['chat_completions','responses','messages','embeddings','images_generations']" :key="kind">{{ kind }}</option></select></label><label>供应商 ID<input v-model="filters.provider_id" name="provider_id" type="number" min="1" /></label><label>请求结果<select v-model="filters.status" name="status"><option value="">全部</option><option value="success">成功</option><option value="error">失败</option></select></label><label>结算状态<select v-model="filters.settlement_status" name="settlement_status"><option value="">全部</option><option v-for="value in ['settled','free','credits_free','unpriced','failed','none']" :key="value" :value="value">{{ settlementLabel(value) }}</option></select></label><label>模型组 ID（分类）<input v-model="filters.model_group_id" name="model_group_id" type="number" min="1" /></label>
        </template>
        <template v-else><label>操作者 ID<input v-model="filters.actor_id" name="actor_id" type="number" min="1" /></label><label>对象 / 资源<input v-model="filters.resource" name="resource" placeholder="providers / aliases / api-keys" /></label><label>动作<input v-model="filters.action" name="action" placeholder="POST / PATCH / DELETE" /></label><label>实际结果<select v-model="filters.result" name="result"><option value="">全部</option><option value="success">成功</option><option value="failure">失败</option><option value="denied">拒绝</option><option value="unknown">未知（历史记录）</option></select></label></template>
        <div class="toolbar-actions wide"><button class="secondary" :disabled="loading">筛选</button><button type="button" class="secondary" :disabled="loading" @click="resetFilters">清空筛选</button></div>
      </form>
      <form v-if="edit" class="panel form-panel" @submit.prevent="save"><h3>{{ edit.id ? '编辑' : '新建' }}{{ titles[view] }}</h3><div class="form-grid"><label>名称<input v-model="edit.name" required maxlength="200" /></label><label v-if="view !== 'routing' || edit.id" class="check"><input v-model="edit.active" type="checkbox" />启用</label>
        <p v-if="view === 'routing'" class="muted wide">{{ edit.id ? '从未启用切换为启用前' : '新模型以未启用草稿保存。启用前' }}必须至少有一条启用路由指向真实存在且启用的供应商。免费合法，未定价只提示；不会自动停用已有异常模型。</p>
        <template v-if="view === 'providers'"><label>协议类型<select v-model="edit.type"><option value="openai_compatible">OpenAI 兼容</option><option value="anthropic">Anthropic</option><option value="gemini">Gemini</option></select></label><label>兼容配置<select v-model="edit.compatibility_profile"><option value="generic">通用</option><option value="deepseek" :disabled="edit.type !== 'openai_compatible'">DeepSeek（仅 OpenAI 兼容）</option></select></label><label>基础 URL<input v-model="edit.base_url" type="url" required placeholder="https://api.example.com/v1" /></label><label>上游密钥<input v-model="edit.api_key" type="password" autocomplete="new-password" :placeholder="edit.id ? '留空保留已有密钥' : '输入上游 API Key'" /></label><label>超时（毫秒）<input v-model.number="edit.timeout_ms" type="number" min="1000" max="600000" required /></label><label>账号并发上限<input v-model.number="edit.max_concurrency" type="number" min="0" max="10000" /><small>0 表示不单独限制</small></label><label class="check"><input v-model="edit.supports_responses" type="checkbox" />支持 Responses API<small>仅在上游实现 /v1/responses 时勾选；其它协议能力未确认时标为未知。</small></label></template>
        <fieldset v-if="view === 'modelGroups'" class="wide"><legend>分类关联的对外模型别名</legend><label v-for="alias in aliases" :key="alias.id" class="check"><input v-model="edit.alias_ids" type="checkbox" :value="alias.id" />{{ alias.name }}{{ alias.active ? '' : '（停用）' }}</label><p v-if="!aliases.length" class="muted">先在别名与路由页面创建模型别名。</p></fieldset>
      </div><div class="toolbar-actions"><button class="primary compact" :disabled="busy">{{ busy ? '保存中…' : '保存' }}</button><button type="button" class="secondary" :disabled="busy" @click="edit = null">取消</button></div></form>
      <p v-if="loading" class="muted" role="status" aria-busy="true">加载中…</p>
      <div v-else-if="error && !rows.length" class="panel empty"><p>未能加载记录，不代表没有数据。</p><button class="secondary" @click="load">重试读取列表</button></div>
      <div v-else-if="!rows.length" class="panel empty">暂无记录</div>
      <div v-else-if="view === 'requests'" class="request-list scroll"><table class="request-table"><thead><tr><th>时间 / 请求 ID（UTC）</th><th>模型 / 协议</th><th>用户 / 密钥</th><th>请求结果</th><th>结算状态</th><th>成本 CNY / 积分</th><th>耗时</th><th>详情</th></tr></thead><tbody><tr v-for="row in rows" :key="row.id" :data-request-id="row.id"><td data-label="时间 / ID"><time>{{ time(row.created_at) }}</time><small class="request-id">{{ row.id }}</small></td><td data-label="模型 / 协议"><strong>{{ row.model_alias || '—' }}</strong><small>{{ row.api || '—' }}</small></td><td data-label="用户 / 密钥">{{ row.user_id ?? '—' }} / {{ row.api_key_id ?? '—' }}</td><td data-label="请求结果"><span :class="{ error: row.status === 'error' }">{{ statusLabel(row.status) }}</span><small v-if="row.status_code">HTTP {{ row.status_code }}</small><small v-if="row.error_summary" class="error" :title="localizeError(row.error_summary)">{{ localizeError(row.error_summary) }}</small></td><td data-label="结算状态">{{ settlementLabel(row.settlement_status) }}</td><td data-label="成本 / 积分">¥{{ row.cost ?? '—' }} / {{ row.credits ?? '—' }}</td><td data-label="耗时">{{ row.total_latency_ms ?? '—' }} ms</td><td class="request-action"><button class="secondary compact" :aria-label="`查看请求 ${row.id} 的详情`" @click="openRow(row, $event)">请求明细</button></td></tr></tbody></table></div>
      <div v-else-if="view === 'audit'" class="op-list audit-list"><article v-for="row in rows" :key="row.id" class="panel op-row audit-row"><div><strong>{{ actorLabel(row) }} · {{ row.action }} · {{ row.resource }}<span v-if="row.resource_id"> #{{ row.resource_id }}</span><span v-if="row.resource_name"> · {{ row.resource_name }}</span></strong><p class="muted">{{ time(row.created_at) }} · IP {{ row.ip || '未记录' }} · 实际结果：{{ auditResult(row.result) }}</p><p v-if="!row.result || row.result === 'unknown'" class="muted">历史审计未记录实际结果；不推断为成功，不展示可能包含敏感字段的旧详情。</p><div class="audit-summaries"><div><h4>变更前（安全摘要）</h4><pre>{{ safeAuditSummary(row.before, row.result) }}</pre></div><div><h4>变更后（安全摘要）</h4><pre>{{ safeAuditSummary(row.after, row.result) }}</pre></div></div></div></article></div>
      <div v-else class="op-list"><article v-for="row in rows" :key="row.id" class="panel op-row"><div><strong>{{ row.name || row.id }}</strong>
        <template v-if="view === 'providers'"><p class="muted">{{ row.base_url }}</p><p class="muted">兼容配置：{{ row.compatibility_profile === 'deepseek' ? 'DeepSeek' : '通用' }}</p><p class="muted">{{ row.supports_responses ? '支持 Responses API' : '不支持 Responses API' }}（配置声明）</p><p class="muted">{{ health(row) }} · 成功 {{ row.success_count ?? 0 }} / 失败 {{ row.failure_count ?? 0 }} · 上次验证 {{ time(row.last_verified_at) }}</p><p class="muted" v-if="row.cooldown_until">冷却截止 {{ time(row.cooldown_until) }}</p><p class="error" v-if="row.last_error">{{ localizeError(row.last_error) }}</p></template>
        <p class="muted" v-if="view === 'modelGroups'">{{ row.active ? '启用' : '停用' }} · {{ row.alias_ids?.length || 0 }} 个关联模型</p>
        <template v-if="view === 'routing'"><p class="muted">{{ row.active ? '已启用' : '未启用草稿' }}<span v-if="readyFor(row)"> · {{ readyFor(row)?.ready ? '就绪' : '有配置待检查' }} · 有效路由 {{ readyFor(row)?.effective_routes }} 条</span></p><template v-if="readyFor(row)"><p class="muted">供应商：{{ readinessProviders(row) }}</p><p class="muted">可确认协议：{{ protocolsLabel(readyFor(row)?.protocols) }}</p><p class="muted">{{ pricingLabel(readyFor(row)?.pricing_status) }}</p><p v-for="reason in readyFor(row)?.reasons || []" :key="reason" class="muted">{{ readinessReason(reason) }}</p></template><p v-else-if="readinessLoading[String(row.id)]" class="muted" role="status">正在读取就绪状态…</p><p v-if="readinessErrors[String(row.id)]" class="error">{{ readinessErrors[String(row.id)] }} <button class="secondary compact" @click="refreshReadiness(row)">重试就绪检查</button></p><div class="toolbar-actions wrap"><button class="secondary compact" @click="emit('navigate', { view: 'providers' })">供应商配置</button><button class="secondary compact" @click="emit('navigate', { view: 'prices', context: { model: row.name } })">价格配置</button></div></template>
      </div><div class="toolbar-actions wrap"><button class="secondary" :disabled="busy" @click="openEditor(row)">编辑</button><button class="secondary" :disabled="busy" @click="toggle(row)">{{ row.active ? '停用' : '启用' }}</button><button v-if="view === 'providers'" class="secondary" :disabled="busy" @click="probe(row, 'verify')">验证连接</button><button v-if="view === 'providers'" class="secondary" :disabled="busy" @click="probe(row, 'discover')">发现模型</button><button v-if="view === 'routing' || view === 'providers'" class="secondary danger" :disabled="busy" @click="removeResource(row)">{{ view === 'routing' ? '删除别名' : '删除' }}</button><button v-if="view !== 'modelGroups'" class="secondary" :disabled="busy" @click="openRow(row, $event)">{{ view === 'routing' ? '配置路由' : '模型目录' }}</button></div></article></div>
      <div v-if="paginated" class="toolbar-actions pager"><button class="secondary" :disabled="page === 1 || loading" @click="changePage(-1)">上一页</button><span>第 {{ page }} 页</span><button class="secondary" :disabled="rows.length < 50 || loading" @click="changePage(1)">下一页</button></div>
      <section v-if="selected && !paginated" class="panel details"><div class="toolbar"><h3>{{ selected.name || selected.id }}</h3><button class="secondary" @click="closeConfig">关闭明细</button></div><p v-if="configLoading" class="muted" role="status">正在读取配置…</p><div v-else-if="configError"><p class="alert" role="alert">{{ configError }}</p><button class="secondary" @click="loadConfig(selected!)">重试读取配置</button></div>
        <template v-else-if="view === 'providers'"><div v-for="model in models" :key="model.id || model.model_id" class="model-row"><strong>{{ model.model_id }}</strong><p class="muted wide">可确认协议：{{ protocolsLabel(model.protocols) }}</p><div class="caps"><label v-for="key in capabilityKeys" :key="key" class="check"><input v-model="model.capabilities[key]" type="checkbox" />{{ capabilityLabels[key] }}</label></div><button class="secondary" :disabled="busy" @click="saveModelCaps(model)">保存能力</button></div><p v-if="!models.length" class="muted">暂无模型，可发现模型或手动添加。</p><form class="form-grid" @submit.prevent="addModel"><label>模型 ID<input v-model="manualModel" required /></label><div class="wide caps"><label v-for="key in capabilityKeys" :key="key" class="check"><input v-model="newModelCaps[key]" type="checkbox" />{{ capabilityLabels[key] }}</label></div><button class="secondary" :disabled="busy">添加模型</button></form></template>
        <template v-else-if="view === 'routing'"><p class="muted">启用模型需至少一条启用路由指向存在且启用的供应商。路由变更不会自动改变模型启停状态；免费合法，未定价只提示。</p><p class="muted">有效路由 {{ readyFor(selected)?.effective_routes ?? '未知' }} 条 · 可确认协议：{{ protocolsLabel(readyFor(selected)?.protocols) }} · {{ pricingLabel(readyFor(selected)?.pricing_status) }}</p><p class="muted">拖动或使用上下按钮调整顺序，保存立即生效。</p><p v-if="!routes.length" class="muted">暂无路由，请先添加路由再启用模型。</p>
          <div v-for="(route, index) in routes" :key="route.id" class="route-row" draggable="true" @dragstart="dragged = index" @dragover.prevent @drop.prevent="drop(index)"><div><strong>{{ route.priority }} · {{ providers.find(provider => provider.id === route.provider_id)?.name || `供应商 ${route.provider_id}（不存在）` }} / {{ route.provider_model }} {{ route.active ? '' : '（路由停用）' }}</strong><p class="muted">供应商：{{ providers.find(provider => provider.id === route.provider_id)?.active ? '启用' : '停用或不存在' }} · 健康：{{ routeReady(route)?.health_status || '未知' }} · 上次验证 {{ time(routeReady(route)?.last_verified_at) }}</p><p class="muted">可确认协议：{{ protocolsLabel(routeReady(route)?.protocols) }} · {{ pricingLabel(routeReady(route)?.pricing_status) }}<span v-if="routeReady(route)?.price_version"> · 价格版本 {{ routeReady(route)?.price_version }}</span></p><div class="toolbar-actions wrap"><button class="secondary compact" @click="emit('navigate', { view: 'providers', context: { detail: String(route.provider_id) } })">供应商 / 模型目录</button><button class="secondary compact" @click="emit('navigate', { view: 'prices', context: { model: routeReady(route)?.price_model_id || route.provider_model } })">路由价格配置</button></div></div><div class="toolbar-actions wrap"><button class="secondary" :disabled="busy || index === 0" :aria-label="`上移 ${route.provider_model}`" @click="move(index, index - 1)">↑</button><button class="secondary" :disabled="busy || index === routes.length - 1" :aria-label="`下移 ${route.provider_model}`" @click="move(index, index + 1)">↓</button><button class="secondary" :disabled="busy" @click="routeActive(route)">{{ route.active ? '停用' : '启用' }}</button><button class="secondary danger" :disabled="busy" @click="removeRoute(route)">移除</button></div></div>
          <form class="form-grid route-form" @submit.prevent="addRoute"><label>供应商<select v-model.number="newRoute.provider_id" name="route_provider"><option :value="0">请选择</option><option v-for="provider in providers" :key="provider.id" :value="provider.id">{{ provider.name }}{{ provider.active ? '' : '（停用）' }}</option></select></label><label>上游模型填写方式<select v-model="modelMode" name="model_mode" @change="newRoute.provider_model = ''"><option value="select">从供应商模型目录选择</option><option value="manual">手工填写（自定义上游模型）</option></select></label><label v-if="modelMode === 'select'">上游模型（目录关联）<select v-model="newRoute.provider_model" name="route_model" :disabled="routeModelLoading"><option value="">{{ routeModelLoading ? '正在读取目录…' : '请选择模型' }}</option><option v-for="model in routeModels" :key="model.model_id" :value="model.model_id">{{ model.model_id }}</option></select><small v-if="!routeModelLoading && !routeModels.length">目录暂无模型，可发现模型或显式切换手工填写。</small></label><label v-else>上游真实模型名（手工填写）<input v-model="newRoute.provider_model" name="route_model_manual" required /><small>按供应商实际模型 ID 填写，不将未确认能力标记为支持。</small></label><p v-if="routeModelError" class="error wide">{{ routeModelError }}</p><label>优先级<input v-model.number="newRoute.priority" type="number" /></label><button class="secondary" :disabled="busy || (modelMode === 'select' && routeModelLoading)">添加路由</button></form>
        </template>
      </section>
      <OperationsRequestDrawer v-if="view === 'requests' && requestId" :request-id="requestId" @close="closeRequest" @navigate="emit('navigate', $event)" />
    </template>
    <div v-else class="panel empty" role="alert">该管理页面不可用或链接已失效。请从当前允许的导航入口访问。</div>
  </div>
</template>

<style scoped>
.op-list{display:grid;gap:12px}.op-row,.route-row{display:flex;justify-content:space-between;align-items:center;gap:16px}.op-row{overflow-wrap:anywhere}.op-row>div:first-child{min-width:0}.wrap{flex-wrap:wrap;flex-shrink:0}.check{display:flex;align-items:center;gap:8px;margin:8px 0}.check input{width:auto}fieldset{border:1px solid var(--border-strong);border-radius:8px;max-height:240px;overflow:auto}legend{color:var(--text-muted)}.details,.pager{margin-top:20px}.route-row{padding:14px 0;border-bottom:1px solid var(--border);flex-wrap:wrap}.details h3{margin:20px 0 12px}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:var(--surface-sunken);border:1px solid var(--border);padding:12px;border-radius:7px;max-height:240px;overflow:auto}.model-row{display:flex;flex-wrap:wrap;gap:10px;align-items:center;justify-content:space-between;padding:12px 0;border-bottom:1px solid var(--border)}.model-row .caps,.form-grid .caps{display:flex;flex-wrap:wrap;gap:12px}.model-row .caps label,.form-grid .caps label{flex-direction:row}.danger{border-color:var(--danger-border);color:var(--danger)}.scroll{overflow-x:auto}table{width:100%;border-collapse:collapse;text-align:left;font-size:12px}th,td{padding:9px 8px;border-bottom:1px solid var(--border)}.request-table{background:var(--surface);table-layout:fixed}.request-table th:first-child{width:23%}.request-table th:nth-child(2){width:16%}.request-table th:nth-child(5){width:14%}.request-table td{vertical-align:top;overflow-wrap:anywhere}.request-table small{display:block;margin-top:4px;color:var(--text-muted);max-height:34px;overflow:hidden}.request-table .error{color:var(--danger)}.request-table time{display:block}.request-table button{white-space:nowrap}.pager{justify-content:center}.filters{margin-bottom:16px}.filters p{margin:0}.audit-row>div{width:100%}.audit-summaries{display:grid;grid-template-columns:1fr 1fr;gap:16px}.audit-summaries h4{font-size:12px;margin:8px 0}.audit-summaries pre{font-size:12px;margin:0}button:disabled{opacity:.5;cursor:default}@media(max-width:1100px){.op-row{align-items:flex-start;flex-direction:column}.wrap{flex-shrink:1}.request-table{table-layout:auto}}@media(max-width:720px){.toolbar{gap:10px;flex-wrap:wrap}.route-row .toolbar-actions{flex-wrap:wrap}.audit-summaries{grid-template-columns:1fr}.request-list{overflow:visible}.request-table,.request-table tbody{display:block}.request-table thead{display:none}.request-table tr{display:grid;grid-template-columns:1fr 1fr;gap:7px 12px;padding:12px;margin-bottom:8px;border:1px solid var(--border);border-radius:8px;background:var(--surface)}.request-table td{display:block;border:0;padding:0;font-size:12px;min-width:0}.request-table td::before{content:attr(data-label);display:block;color:var(--text-muted);font-size:11px;margin-bottom:3px}.request-table td:first-child{grid-column:1/-1}.request-table td:first-child time,.request-table td:first-child small{display:inline;margin-right:8px}.request-action{justify-self:end;align-self:end}.request-table .request-action::before{display:none}.request-table .request-id{overflow-wrap:anywhere}}
</style>
