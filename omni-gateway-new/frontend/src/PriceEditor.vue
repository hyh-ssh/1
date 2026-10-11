<script setup lang="ts">
import { computed, onBeforeUnmount, onBeforeMount, ref, useId, watch } from 'vue'
import { api } from './api'
import {
  freePriceMaps, meterIsToken, meterLabel, parseRateMap, priceClassification,
  priceMapsToRows, priceRowsToMaps, quotePrice, rateUnit, resolvePrice,
  suggestNextVersion, validatePriceMaps, validateRate,
  type PriceClassification, type PriceMaps,
} from './priceMath'

const props = defineProps<{
  prices?: any[]
  modelOptions?: { value: string; label: string }[]
  initialModel?: string
}>()
const emit = defineEmits<{ saved: [price: any]; cancel: [] }>()
const id = useId()
const defaults: PriceMaps = {
  cost: { input: '0.000001', output: '0.000002' },
  credits: { input: '0.0002', output: '0.0004' },
}
const model = ref(props.initialModel || '')
const version = ref('1')
const versionEdited = ref(false)
const mode = ref<'table' | 'json'>('table')
const tableRows = ref(priceMapsToRows(defaults))
const costJSON = ref(JSON.stringify(defaults.cost, null, 2))
const creditsJSON = ref(JSON.stringify(defaults.credits, null, 2))
const newMeter = ref('')
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
const error = ref('')
const notice = ref('')
const loadedPrices = ref<any[]>([])
const locallySaved = ref<any[]>([])
let disposed = false
onBeforeUnmount(() => { disposed = true })
const prices = computed(() => [...(props.prices ?? loadedPrices.value), ...locallySaved.value])
const suggestedVersion = computed(() => suggestNextVersion(prices.value, model.value.trim()))
const blocked = computed(() => loading.value || saving.value || !!loadError.value)
const alreadySaved = computed(() => locallySaved.value.some(price => price.model_id === model.value.trim() && String(price.version) === version.value))
const options = computed(() => {
  const known = new Map((props.modelOptions || []).map(option => [option.value, option]))
  for (const price of prices.value) {
    if (typeof price?.model_id === 'string' && !known.has(price.model_id)) {
      known.set(price.model_id, { value: price.model_id, label: `已有价格 · ${price.model_id}（类型待确认）` })
    }
  }
  return [...known.values()]
})
const selectedOption = computed(() => options.value.some(option => option.value === model.value) ? model.value : '')

function clearFeedback() { error.value = ''; notice.value = '' }
watch([model, version, costJSON, creditsJSON], clearFeedback)
watch(tableRows, clearFeedback, { deep: true })
watch(model, () => {
  versionEdited.value = false
  version.value = String(suggestedVersion.value)
})
watch(suggestedVersion, next => {
  if (!versionEdited.value) version.value = String(next)
}, { immediate: true })
watch(() => props.initialModel, next => {
  if (next !== undefined && !saving.value) model.value = next
})
watch(() => props.prices, next => {
  if (next !== undefined) { loading.value = false; loadError.value = '' }
  else void loadPrices()
})

async function loadPrices() {
  if (loading.value || saving.value || props.prices !== undefined) return
  loading.value = true
  loadError.value = ''
  try {
    const result = await api.resource<any[]>('admin/prices')
    if (disposed || props.prices !== undefined) return
    if (result.error) throw new Error(result.error)
    if (!Array.isArray(result.data)) throw new Error('价格列表响应格式无效，不能确认版本或适用价格。')
    loadedPrices.value = result.data
  } catch (caught) {
    if (!disposed && props.prices === undefined) loadError.value = message(caught, '价格读取失败，请重试。')
  } finally {
    if (!disposed) loading.value = false
  }
}
onBeforeMount(() => { if (props.prices === undefined) void loadPrices() })

function message(caught: unknown, fallback = '价格配置无效。'): string {
  return caught instanceof Error ? caught.message : fallback
}
function inputValue(event: Event): string { return (event.target as HTMLInputElement).value }
function changeVersion(event: Event) {
  versionEdited.value = true
  version.value = inputValue(event)
  clearFeedback()
}
function draft(allowIncomplete = false): PriceMaps {
  if (mode.value === 'table') return priceRowsToMaps(tableRows.value, allowIncomplete)
  return allowIncomplete
    ? { cost: parseRateMap(costJSON.value, '成本', true), credits: parseRateMap(creditsJSON.value, '积分', true) }
    : validatePriceMaps(costJSON.value, creditsJSON.value)
}
function applyMaps(maps: PriceMaps) {
  const rows = priceMapsToRows(maps) // Build/validate everything before replacing either ledger.
  tableRows.value = rows
  costJSON.value = JSON.stringify(maps.cost, null, 2)
  creditsJSON.value = JSON.stringify(maps.credits, null, 2)
}
function changeMode(next: 'table' | 'json') {
  if (blocked.value || mode.value === next) return
  clearFeedback()
  try {
    applyMaps(draft())
    mode.value = next
  } catch (caught) { error.value = message(caught) }
}
function setFree(creditsOnly = false) {
  if (blocked.value) return
  clearFeedback()
  try {
    const maps = draft(true)
    applyMaps(freePriceMaps(maps.cost, maps.credits, creditsOnly))
  } catch (caught) { error.value = message(caught) }
}
function addMeter() {
  if (blocked.value) return
  clearFeedback()
  const meter = newMeter.value.trim()
  if (!meter) { error.value = '计量项名称不能为空。'; return }
  if (tableRows.value.some(row => row.meter === meter)) { error.value = `计量项 ${meter} 重复。`; return }
  tableRows.value.push({ meter, cost: '0', credits: '0' })
  newMeter.value = ''
}
function removeMeter(index: number) {
  if (!blocked.value) { tableRows.value.splice(index, 1); clearFeedback() }
}
function rateError(index: number, ledger: 'cost' | 'credits'): string {
  const row = tableRows.value[index]!
  if (row[ledger] === '') return ledger === 'credits' && row.cost !== ''
    ? `积分 / ${row.meter}：成本已配置，积分必填；免积分请填 0。` : ''
  try { validateRate(row[ledger], `${ledger === 'cost' ? '成本' : '积分'} / ${row.meter}`); return '' }
  catch (caught) { return message(caught) }
}
function jsonError(ledger: 'cost' | 'credits'): string {
  try {
    const rates = parseRateMap(ledger === 'cost' ? costJSON.value : creditsJSON.value, ledger === 'cost' ? '成本' : '积分')
    if (ledger === 'credits') {
      let cost: Record<string, string>
      try { cost = parseRateMap(costJSON.value, '成本') } catch { return '' }
      for (const meter of Object.keys(cost)) {
        if (!Object.prototype.hasOwnProperty.call(rates, meter)) return `积分 / ${meter}：成本已配置，必须填写积分费率；免积分请填 "0"。`
      }
    }
    return ''
  } catch (caught) { return message(caught) }
}
const draftState = computed(() => {
  try { return { maps: draft(), error: '' } }
  catch (caught) { return { maps: null, error: message(caught) } }
})
const classificationLabels: Record<PriceClassification, string> = {
  free: '免费', credits_free: '免积分（仍计成本）', priced: '已定价', unpriced: '未定价（不是免费）', invalid: '配置无效',
}
const draftClassification = computed(() => draftState.value.maps ? priceClassification(draftState.value.maps) : 'invalid')

const quoteSource = ref<'draft' | 'matching'>('draft')
function quantityError(meter: string): string {
  return /^\d+$/.test(quantities.value[meter] ?? '0') ? '' : `${meter} 数量必须为非负整数。`
}
const quoteProviderModel = ref('')
const quoteAlias = ref('')
const quantities = ref<Record<string, string>>(Object.assign(Object.create(null), { input: '1000', output: '1000' }))
const matching = computed(() => resolvePrice(prices.value, quoteProviderModel.value.trim(), quoteAlias.value.trim()))
const quoteState = computed(() => {
  if (quoteSource.value === 'draft') return draftState.value
  if (loading.value || loadError.value) return { maps: null, error: '价格尚未读取成功，不能确认适用版本。' }
  if (!matching.value.price) return { maps: null, error: '未定价（不是免费）：请填写实际上游模型名与对外别名，或创建价格版本。' }
  try { return { maps: validatePriceMaps(matching.value.price.cost, matching.value.price.credits), error: '' } }
  catch (caught) { return { maps: null, error: `适用价格配置无效：${message(caught)}` } }
})
const quoteMeters = computed(() => quoteState.value.maps
  ? [...new Set([...Object.keys(quoteState.value.maps.cost), ...Object.keys(quoteState.value.maps.credits)])] : [])
const quote = computed(() => {
  if (!quoteState.value.maps) return { result: null, error: quoteState.value.error }
  try { return { result: quotePrice(quoteState.value.maps, quantities.value), error: '' } }
  catch (caught) { return { result: null, error: message(caught) } }
})
function duplicateMessage(minimumNext = 0): string {
  return `相同模型与版本已存在；历史价格不可修改，请选择新版本（建议尝试 v${Math.max(suggestedVersion.value, minimumNext)}，最终以服务端校验为准）。`
}
async function save() {
  if (blocked.value || alreadySaved.value) return
  clearFeedback()
  try {
    const modelID = model.value.trim()
    if (!modelID) throw new Error('模型 ID 必填；请选择关联模型或填写实际上游模型名 / 对外别名。')
    // Versions are API integer metadata, not decimal rates or amounts.
    if (!/^\d+$/.test(version.value) || !Number.isSafeInteger(Number(version.value)) || Number(version.value) < 1 || Number(version.value) > 2147483647) {
      throw new Error('版本必须为 1–2147483647 的整数。')
    }
    const versionNumber = Number(version.value)
    if (prices.value.some(price => price.model_id === modelID && price.version === versionNumber)) throw new Error(duplicateMessage())
    const maps = draft()
    const payload = { model_id: modelID, version: versionNumber, cost: maps.cost, credits: maps.credits }
    saving.value = true
    const result = await api.resource('admin/prices', { method: 'POST', body: JSON.stringify(payload) })
    if (disposed) return
    if (result.error) {
      if (result.status === 409 || /duplicate|unique|23505|已存在|conflict/i.test(result.error)) throw new Error(duplicateMessage(versionNumber + 1))
      throw new Error(result.error)
    }
    // Do not auto-increment/resubmit the successful snapshot or mutate props.
    versionEdited.value = true
    locallySaved.value.push(payload)
    notice.value = '价格版本已创建。调整价格请另建更高版本，已有价格和历史账本不会改写。'
    emit('saved', result.data ?? payload)
  } catch (caught) { if (!disposed) error.value = message(caught, '保存失败，请显式重试。') }
  finally { if (!disposed) saving.value = false }
}
</script>

<template>
  <article class="panel form-panel price-editor" :aria-labelledby="`${id}-title`" :aria-busy="loading || saving">
    <h3 :id="`${id}-title`">新建价格版本</h3>
    <p class="muted">价格版本是不可变快照，创建后不可修改或删除；调整请新建更高版本，不追溯修改历史价格或账本。</p>
    <p class="muted">先匹配实际上游模型名的最高版本价格，再回退对外别名的最高版本价格；别名价不会覆盖已有上游价。同名上游模型共用价格，不支持供应商专属定价键。</p>
    <p v-if="loading" role="status" class="muted">价格加载中…</p>
    <div v-else-if="loadError" role="alert" class="alert">{{ loadError }} <button type="button" @click="loadPrices">重试读取价格</button></div>
    <div v-if="error" role="alert" class="alert">{{ error }}</div>
    <div v-if="notice" role="status" class="notice">{{ notice }}</div>
    <form @submit.prevent="save" novalidate>
      <fieldset :disabled="blocked">
        <legend class="sr-only">新价格配置</legend>
        <div class="form-grid">
          <label v-if="options.length">关联已知模型
            <select aria-label="关联已知模型" :value="selectedOption" @change="model = inputValue($event)">
              <option value="">手工填写上游模型 / 别名</option>
              <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
          <label>模型 ID（支持手工填写上游模型名 / 对外别名）<input v-model="model" aria-label="模型 ID" autocomplete="off" /></label>
          <label>新版本<input :value="version" aria-label="版本" inputmode="numeric" @input="changeVersion" />
            <small>按当前已知价格建议 v{{ suggestedVersion }}；保存时以服务端唯一性校验为准。</small>
          </label>
        </div>
        <div class="toolbar-actions modes" role="group" aria-label="价格编辑模式">
          <button type="button" class="secondary compact" :aria-pressed="mode === 'table'" @click="changeMode('table')">价格表格</button>
          <button type="button" class="secondary compact" :aria-pressed="mode === 'json'" @click="changeMode('json')">高级 JSON</button>
          <span class="tag" :class="{ inactive: draftClassification === 'invalid' }" data-testid="draft-classification">{{ classificationLabels[draftClassification] }}</span>
        </div>
        <template v-if="mode === 'table'">
          <p class="muted">已知 Token 项按每百万 Token 输入，保存时精确转换为每 Token 十进制字符串。其他计量项按上报的实际单位，不进行 Token 换算。空白表示缺少费率，不是 0。</p>
          <div class="price-scroll" tabindex="0" role="region" aria-label="价格费率表，可横向滚动">
            <table class="price-table">
              <caption class="sr-only">成本与积分独立配置；成本以元/CNY，积分以积分计价。</caption>
              <thead><tr><th scope="col">计量项 / 单位</th><th scope="col">成本（CNY）</th><th scope="col">积分费率（积分）</th><th scope="col">操作</th></tr></thead>
              <tbody><tr v-for="(row, index) in tableRows" :key="row.meter">
                <th scope="row">{{ meterLabel(row.meter) }}<small>{{ rateUnit(row.meter) }}</small></th>
                <td><label>成本（CNY）<input v-model="row.cost" type="text" inputmode="decimal" :data-meter="row.meter" data-ledger="cost" :aria-label="`成本 CNY：${row.meter}，${rateUnit(row.meter)}`" :aria-invalid="!!rateError(index, 'cost')" :aria-describedby="rateError(index, 'cost') ? `${id}-cost-${index}` : undefined" placeholder="未配置（不是 0）" />
                  <small v-if="rateError(index, 'cost')" :id="`${id}-cost-${index}`" class="field-error">{{ rateError(index, 'cost') }}</small></label></td>
                <td><label>积分<input v-model="row.credits" type="text" inputmode="decimal" :data-meter="row.meter" data-ledger="credits" :aria-label="`积分：${row.meter}，${rateUnit(row.meter)}`" :aria-invalid="!!rateError(index, 'credits')" :aria-describedby="rateError(index, 'credits') ? `${id}-credits-${index}` : undefined" placeholder="免积分请填 0" />
                  <small v-if="rateError(index, 'credits')" :id="`${id}-credits-${index}`" class="field-error">{{ rateError(index, 'credits') }}</small></label></td>
                <td><button type="button" class="secondary compact" :aria-label="`移除 ${row.meter} 计量项`" @click="removeMeter(index)">移除</button></td>
              </tr></tbody>
            </table>
          </div>
          <div class="add-meter">
            <label>新增计量项名称<input v-model="newMeter" aria-label="新增计量项名称" placeholder="实际用量中的计量项键，例如 image" @keydown.enter.prevent="addMeter" /></label>
            <button type="button" class="secondary compact" @click="addMeter">添加计量项</button>
          </div>
          <p class="muted">自定义项单位以用量上报为准；添加后默认两账为 0，请核对费率后保存。缓存 / 推理可能是输入输出的子集，按实际计价项试算，不重复添加聚合量。</p>
        </template>
        <div v-else class="form-grid">
          <p class="muted wide">高级 JSON 使用 API 每实际单位费率（Token 为每 Token），不是每百万 Token。两表必须非空，积分须覆盖成本的每个计量项；费率必须带引号，支持非负小数 / 指数以及显式 "0"。</p>
          <label class="wide">成本 JSON（CNY 每单位）<textarea v-model="costJSON" aria-label="成本 JSON（CNY 每单位）" :aria-invalid="!!jsonError('cost')" :aria-describedby="jsonError('cost') ? `${id}-cost-json` : undefined" spellcheck="false" />
            <small v-if="jsonError('cost')" :id="`${id}-cost-json`" class="field-error">{{ jsonError('cost') }}</small></label>
          <label class="wide">积分 JSON（积分每单位）<textarea v-model="creditsJSON" aria-label="积分 JSON（积分每单位）" :aria-invalid="!!jsonError('credits')" :aria-describedby="jsonError('credits') ? `${id}-credits-json` : undefined" spellcheck="false" />
            <small v-if="jsonError('credits')" :id="`${id}-credits-json`" class="field-error">{{ jsonError('credits') }}</small></label>
        </div>
        <div class="toolbar-actions free-actions">
          <button type="button" class="secondary" @click="setFree(false)">设为免费：成本和积分均为 0</button>
          <button type="button" class="secondary" @click="setFree(true)">仅免积分：保留成本</button>
        </div>
        <p class="muted">快捷操作只填写当前草稿，不自动保存；保留 input/output 和两账全部自定义计量项。免费调用仍记录用量、Token、耗时和价格版本；仍须鉴权、模型授权，并受请求数和 Token 限流及并发限制约束。</p>
      </fieldset>

      <section class="quote-panel" :aria-labelledby="`${id}-quote-title`">
        <h3 :id="`${id}-quote-title`">本地双账试算</h3>
        <p class="muted">不发起模型请求，不产生真实用量；不存在人民币/积分汇率。按单次成功尝试计算，重试 / 失败 / 估算用量以实际请求与账本为准。</p>
        <label class="quote-source">试算价格来源<select v-model="quoteSource" aria-label="试算价格来源" :disabled="saving">
          <option value="draft">当前未保存草稿</option><option value="matching">按已知价格匹配上游模型 / 别名</option>
        </select></label>
        <p v-if="quoteSource === 'draft'" class="muted">仅按草稿试算，不代表任何路由已经使用此价格。</p>
        <template v-else>
          <div class="form-grid">
            <label>实际上游模型名（优先）<input v-model="quoteProviderModel" aria-label="实际上游模型名（试算）" :list="`${id}-models`" :disabled="saving" /></label>
            <label>对外别名（回退）<input v-model="quoteAlias" aria-label="对外别名（试算）" :list="`${id}-models`" :disabled="saving" /></label>
          </div>
          <datalist :id="`${id}-models`"><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></datalist>
          <p v-if="matching.price && !loading && !loadError" class="muted" data-testid="matching-price">适用价格：{{ matching.source === 'upstream' ? '上游模型' : '别名回退' }} {{ matching.price.model_id }} · v{{ matching.price.version }} · {{ classificationLabels[priceClassification(matching.price)] }}</p>
          <p v-if="!quoteProviderModel.trim()" class="muted">未提供实际上游模型名，无法确认别名价格是否会被上游价覆盖；这里只能展示别名候选价。</p>
        </template>
        <div v-if="quoteMeters.length" class="form-grid">
          <label v-for="(meter, index) in quoteMeters" :key="meter">{{ meterLabel(meter) }} 数量（{{ meterIsToken(meter) ? '实际 Token' : '实际计量单位' }}）
            <input :value="quantities[meter] ?? '0'" :aria-label="`试算数量：${meter}`" inputmode="numeric" :disabled="saving" :aria-invalid="!!quantityError(meter)" :aria-describedby="quantityError(meter) ? `${id}-quantity-${index}` : undefined" @input="quantities[meter] = inputValue($event)" />
            <small v-if="quantityError(meter)" :id="`${id}-quantity-${index}`" class="field-error">{{ quantityError(meter) }}</small>
          </label>
        </div>
        <p v-if="quote.error" class="muted" role="status">试算不可用：{{ quote.error }}</p>
        <div v-else-if="quote.result" class="quote-results" aria-live="polite">
          <strong data-testid="quote-cost">¥{{ quote.result.costDisplay }} CNY</strong>
          <strong data-testid="quote-credits">{{ quote.result.credits }} 积分</strong>
          <details><summary>精确值与结算口径</summary>
            <p class="muted">成本仅为可读显示保留 6 位小数，账本保留完整精度；积分逐计量项四舍五入到 3 位（半值进位）再相加。费率本身不舍入，显示为 0 不代表显式免费。</p>
            <p class="muted">成本精确值（CNY）：<code data-testid="quote-cost-exact">{{ quote.result.costExact }}</code></p>
            <p class="muted">积分未舍入合计：<code>{{ quote.result.creditsExact }}</code> 积分</p>
          </details>
        </div>
      </section>
      <div class="toolbar-actions form-actions">
        <button type="submit" class="primary compact" :disabled="blocked || alreadySaved">{{ saving ? '保存中…' : alreadySaved ? '此版本已保存' : '保存新版本' }}</button>
        <button type="button" class="secondary" :disabled="saving" @click="emit('cancel')">取消</button>
      </div>
    </form>
  </article>
</template>

<style scoped>
.price-editor { min-width: 0; }
.price-editor fieldset { border: 0; padding: 0; margin: 0; min-width: 0; }
.price-editor .alert, .price-editor .notice { margin-top: 16px; }
.price-editor small { color: var(--text-muted); line-height: 1.5; overflow-wrap: anywhere; }
.price-editor .field-error { color: var(--danger); }
.price-editor input[aria-invalid="true"], .price-editor textarea[aria-invalid="true"] { border-color: var(--danger); }
.price-editor button:focus-visible, .price-scroll:focus-visible, .price-editor summary:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
.modes, .free-actions, .form-actions { flex-wrap: wrap; margin-top: 16px; }
.price-scroll { overflow-x: auto; margin-top: 12px; border: 1px solid var(--border); border-radius: 7px; }
.price-table { width: 100%; min-width: 650px; border-collapse: collapse; text-align: left; }
.price-table th, .price-table td { padding: 12px; vertical-align: top; border-bottom: 1px solid var(--border); }
.price-table thead { background: var(--surface-muted); color: var(--text-muted); font-size: 12px; }
.price-table tbody th { width: 24%; font-size: 13px; font-weight: 500; overflow-wrap: anywhere; }
.price-table th small { display: block; margin-top: 5px; font-weight: 400; }
.price-table td { min-width: 190px; }
.price-table td:last-child { min-width: 70px; }
.price-table tr:last-child th, .price-table tr:last-child td { border-bottom: 0; }
.add-meter { display: flex; align-items: end; gap: 12px; margin-top: 14px; }
.add-meter label { flex: 1; max-width: 420px; }
.price-editor textarea { min-height: 140px; font-family: ui-monospace, monospace; }
.quote-panel { border-top: 1px solid var(--border); margin-top: 24px; padding-top: 20px; }
.quote-source { margin-top: 14px; max-width: 440px; }
.quote-results { display: flex; gap: 14px; flex-wrap: wrap; margin-top: 14px; }
.quote-results strong { border: 1px solid var(--border); background: var(--surface-muted); border-radius: 7px; padding: 12px 16px; overflow-wrap: anywhere; }
.quote-results details { flex-basis: 100%; color: var(--text-muted); font-size: 13px; }
.quote-results summary { cursor: pointer; }
.quote-results code { overflow-wrap: anywhere; color: var(--text); }
@media (max-width: 600px) { .add-meter { flex-wrap: wrap; } .add-meter label { flex-basis: 100%; } }
</style>
