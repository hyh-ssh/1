import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import PriceEditor from './PriceEditor.vue'

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(body) }
}
type Call = { url: string; method: string; body?: any }
let calls: Call[] = []
let wrappers: VueWrapper[] = []
function server(handler: (call: Call) => any = call => response({ id: 7, ...call.body }, 201)) {
  vi.stubGlobal('fetch', vi.fn((url: string, init: RequestInit = {}) => {
    const call = { url, method: init.method || 'GET', body: init.body ? JSON.parse(String(init.body)) : undefined }
    calls.push(call)
    return Promise.resolve(handler(call))
  }))
}
function editor(props: Record<string, any> = {}) {
  const wrapper = mount(PriceEditor, { props: { prices: [], initialModel: 'upstream', ...props } })
  wrappers.push(wrapper)
  return wrapper
}
function button(wrapper: VueWrapper, text: string) {
  return wrapper.findAll('button').find(item => item.text() === text)!
}
function rate(wrapper: VueWrapper, ledger: string, meter: string) {
  return wrapper.findAll('input').find(input => input.attributes('data-ledger') === ledger && input.attributes('data-meter') === meter)!
}
function value(field: ReturnType<VueWrapper['get']>) {
  return (field.element as HTMLInputElement).value
}
async function advanced(wrapper: VueWrapper, cost?: string, credits?: string) {
  await button(wrapper, '高级 JSON').trigger('click')
  if (cost !== undefined) await wrapper.get('textarea[aria-label="成本 JSON（CNY 每单位）"]').setValue(cost)
  if (credits !== undefined) await wrapper.get('textarea[aria-label="积分 JSON（积分每单位）"]').setValue(credits)
}
async function save(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}
const defaultMaps = { cost: { input: '0.000001', output: '0.000002' }, credits: { input: '0.0002', output: '0.0004' } }

beforeEach(() => { calls = []; wrappers = []; sessionStorage.clear(); server() })
afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe('business price table and immutable API payload', () => {
  it('displays separate per-million rates, named association, and next version without reading or mutating existing rows', async () => {
    const prices = [{ id: 2, model_id: 'upstream', version: 3, ...defaultMaps }]
    const snapshot = JSON.stringify(prices)
    const wrapper = editor({ prices, modelOptions: [{ value: 'upstream', label: '上游模型 · upstream' }] })
    expect(value(rate(wrapper, 'cost', 'input'))).toBe('1')
    expect(value(rate(wrapper, 'credits', 'input'))).toBe('200')
    expect(wrapper.text()).toContain('每百万 Token')
    expect(wrapper.text()).toContain('不可修改或删除')
    expect(value(wrapper.get('input[aria-label="版本"]'))).toBe('4')
    expect(wrapper.get('select[aria-label="关联已知模型"]').text()).toContain('上游模型 · upstream')
    await save(wrapper)
    expect(calls).toEqual([{ url: '/api/admin/prices', method: 'POST', body: { model_id: 'upstream', version: 4, ...defaultMaps } }])
    expect(wrapper.emitted('saved')?.[0]?.[0]).toMatchObject({ id: 7, model_id: 'upstream', version: 4 })
    expect(JSON.stringify(prices)).toBe(snapshot)
  })

  it('supports a manual upstream model and exact table edits including custom actual-unit rates', async () => {
    const wrapper = editor()
    await wrapper.get('input[aria-label="模型 ID"]').setValue('manual-model')
    await rate(wrapper, 'cost', 'input').setValue('1.234567890123456789')
    await rate(wrapper, 'credits', 'input').setValue('2e-1000')
    await wrapper.get('input[aria-label="新增计量项名称"]').setValue('vendor/custom')
    await button(wrapper, '添加计量项').trigger('click')
    await rate(wrapper, 'cost', 'vendor/custom').setValue('1.000000000000000000001e-9')
    await rate(wrapper, 'credits', 'vendor/custom').setValue('0')
    expect(rate(wrapper, 'cost', 'vendor/custom').attributes('aria-label')).toContain('每 1 实际计量单位')
    await save(wrapper)
    expect(calls[0]!.body.model_id).toBe('manual-model')
    expect(calls[0]!.body.cost.input).toBe('0.000001234567890123456789')
    expect(calls[0]!.body.credits.input).not.toBe('0')
    expect(calls[0]!.body.cost['vendor/custom']).toBe('1.000000000000000000001e-9')
    expect(typeof calls[0]!.body.cost.input).toBe('string')
  })

  it('roundtrips advanced JSON/table repeatedly without losing exact strings or credits-only custom keys', async () => {
    const wrapper = editor()
    const cost = { input: '1.23000e-1000', image: '9007199254740993.00000000000000000001' }
    const credits = { input: '+0.000000000000000000001', image: '0', 'custom-only': '2.3456000e-99' }
    await advanced(wrapper, JSON.stringify(cost), JSON.stringify(credits))
    for (let index = 0; index < 3; index++) {
      await button(wrapper, '价格表格').trigger('click')
      expect(value(rate(wrapper, 'cost', 'image'))).toBe(cost.image)
      expect(value(rate(wrapper, 'cost', 'custom-only'))).toBe('')
      await button(wrapper, '高级 JSON').trigger('click')
      expect(JSON.parse(value(wrapper.get('textarea[aria-label="成本 JSON（CNY 每单位）"]')))).toEqual(cost)
      expect(JSON.parse(value(wrapper.get('textarea[aria-label="积分 JSON（积分每单位）"]')))).toEqual(credits)
    }
    await save(wrapper)
    expect(calls[0]!.body).toMatchObject({ cost, credits })
  })

  it('rejects duplicate custom names and missing credits with accessible field errors, not an implicit zero', async () => {
    const wrapper = editor()
    await wrapper.get('input[aria-label="新增计量项名称"]').setValue('input')
    await button(wrapper, '添加计量项').trigger('click')
    expect(wrapper.get('[role="alert"]').text()).toContain('重复')
    await rate(wrapper, 'credits', 'input').setValue('')
    expect(rate(wrapper, 'credits', 'input').attributes('aria-invalid')).toBe('true')
    await save(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toContain('input')
    expect(calls).toHaveLength(0)
    await rate(wrapper, 'credits', 'input').setValue('0')
    await save(wrapper)
    expect(calls[0]!.body.credits.input).toBe('0')
  })

  it.each([
    ['{broken', '{"input":"0"}', 'JSON 格式无效'],
    ['[]', '{"input":"0"}', '对象'],
    ['{}', '{"input":"0"}', '至少'],
    ['{"input":"0"}', 'null', '积分'],
    ['{"input":1e-1000}', '{"input":"0"}', '十进制字符串'],
    ['{"input":"-1e-1000"}', '{"input":"0"}', '非负'],
    ['{"input":"0"}', '{"output":"0"}', 'input'],
  ])('blocks invalid JSON/mode changes/save without partial rewrites (%s)', async (cost, credits, message) => {
    const wrapper = editor()
    await advanced(wrapper, cost, credits)
    await button(wrapper, '价格表格').trigger('click')
    expect(wrapper.findAll('textarea')).toHaveLength(2)
    expect(wrapper.get('[role="alert"]').text()).toContain(message)
    expect(value(wrapper.findAll('textarea')[0]!)).toBe(cost)
    expect(value(wrapper.findAll('textarea')[1]!)).toBe(credits)
    await save(wrapper)
    expect(calls).toHaveLength(0)
  })
})

describe('explicit free and independent local quote', () => {
  it('zeros the union including custom fields without auto-saving, then saves an explicit free version', async () => {
    const wrapper = editor()
    await advanced(wrapper, '{"input":"0.01","image":"2"}', '{"output":"0.02","custom":"3"}')
    await button(wrapper, '设为免费：成本和积分均为 0').trigger('click')
    const expected = { input: '0', output: '0', image: '0', custom: '0' }
    expect(JSON.parse(value(wrapper.findAll('textarea')[0]!))).toEqual(expected)
    expect(JSON.parse(value(wrapper.findAll('textarea')[1]!))).toEqual(expected)
    expect(wrapper.get('[data-testid="draft-classification"]').text()).toBe('免费')
    expect(wrapper.text()).toContain('免费调用仍记录用量')
    expect(calls).toHaveLength(0)
    await save(wrapper)
    expect(calls[0]!.body).toMatchObject({ cost: expected, credits: expected })
  })

  it('waives credits while keeping cost exact, and refuses invalid free operations atomically', async () => {
    const wrapper = editor()
    const cost = '{"input":"1e-1000","image":"2.000000000001"}'
    await advanced(wrapper, cost, '{"input":"1","image":"4"}')
    await button(wrapper, '仅免积分：保留成本').trigger('click')
    expect(JSON.parse(value(wrapper.findAll('textarea')[0]!))).toEqual(JSON.parse(cost))
    expect(JSON.parse(value(wrapper.findAll('textarea')[1]!))).toEqual({ input: '0', output: '0', image: '0' })
    expect(wrapper.get('[data-testid="draft-classification"]').text()).toBe('免积分（仍计成本）')
    await wrapper.findAll('textarea')[1]!.setValue('{broken')
    const before = wrapper.findAll('textarea').map(field => value(field))
    await button(wrapper, '设为免费：成本和积分均为 0').trigger('click')
    expect(wrapper.findAll('textarea').map(field => value(field))).toEqual(before)
    expect(wrapper.get('[role="alert"]').text()).toContain('积分 JSON 格式无效')
    expect(calls).toHaveLength(0)
  })

  it.each(['{"input":1e-1000}', '{"input":"-1e-1000"}', 'null'])('does not repair invalid rates into free implicitly (%s)', async invalid => {
    const wrapper = editor()
    await advanced(wrapper, invalid, '{"input":"0.0002"}')
    const before = wrapper.findAll('textarea').map(field => value(field))
    await button(wrapper, '设为免费：成本和积分均为 0').trigger('click')
    expect(wrapper.findAll('textarea').map(field => value(field))).toEqual(before)
    expect(wrapper.get('[role="alert"]').text()).toContain('成本')
    expect(calls).toHaveLength(0)
  })

  it('marks invalid quote quantities accessibly without changing or saving price rates', async () => {
    const wrapper = editor()
    const field = wrapper.get('input[aria-label="试算数量：input"]')
    await field.setValue('-1')
    expect(field.attributes('aria-invalid')).toBe('true')
    expect(wrapper.text()).toContain('数量必须为非负整数')
    expect(wrapper.find('[data-testid="quote-cost"]').exists()).toBe(false)
    expect(value(rate(wrapper, 'cost', 'input'))).toBe('1')
    expect(calls).toHaveLength(0)
  })

  it('quotes independently with actual quantities, correct per-meter credit rounding, and no requests', async () => {
    const wrapper = editor()
    await wrapper.get('input[aria-label="试算数量：output"]').setValue('2000')
    expect(wrapper.get('[data-testid="quote-cost"]').text()).toBe('¥0.005000 CNY')
    expect(wrapper.get('[data-testid="quote-credits"]').text()).toBe('1.000 积分')
    expect(wrapper.text()).toContain('不发起模型请求')
    expect(wrapper.text()).toContain('不存在人民币/积分汇率')
    await advanced(wrapper, '{"image":"0.000000123456789","custom":"0.25"}', '{"image":"0.0005","custom":"0.0005"}')
    await wrapper.get('input[aria-label="试算数量：image"]').setValue('1')
    await wrapper.get('input[aria-label="试算数量：custom"]').setValue('1')
    expect(wrapper.get('[data-testid="quote-cost-exact"]').text()).toBe('0.250000123456789')
    expect(wrapper.get('[data-testid="quote-credits"]').text()).toBe('0.002 积分')
    expect(calls).toHaveLength(0)
  })

  it('does not label a tiny positive rate free when its display/rounded amount is zero', async () => {
    const wrapper = editor()
    await advanced(wrapper, '{"input":"1e-1000"}', '{"input":"1e-1000"}')
    expect(wrapper.get('[data-testid="draft-classification"]').text()).toBe('已定价')
    expect(wrapper.get('[data-testid="quote-credits"]').text()).toBe('0.000 积分')
    expect(wrapper.text()).toContain('显示为 0 不代表显式免费')
    expect(calls).toHaveLength(0)
  })

  it('explains and displays upstream-first/alias-fallback matching with actual known version; no invented provider key', async () => {
    const wrapper = editor({ prices: [
      { model_id: 'alias', version: 9, cost: { input: '0.01' }, credits: { input: '2' } },
      { model_id: 'upstream', version: 3, cost: { input: '0.001' }, credits: { input: '0.5' } },
      { model_id: 'provider:upstream', version: 99, ...defaultMaps },
    ] })
    await wrapper.get('select[aria-label="试算价格来源"]').setValue('matching')
    await wrapper.get('input[aria-label="实际上游模型名（试算）"]').setValue('upstream')
    await wrapper.get('input[aria-label="对外别名（试算）"]').setValue('alias')
    expect(wrapper.get('[data-testid="matching-price"]').text()).toContain('上游模型 upstream · v3')
    expect(wrapper.get('[data-testid="quote-cost"]').text()).toBe('¥1.000000 CNY')
    await wrapper.get('input[aria-label="实际上游模型名（试算）"]').setValue('missing')
    expect(wrapper.get('[data-testid="matching-price"]').text()).toContain('别名回退 alias · v9')
    expect(wrapper.get('[data-testid="quote-cost"]').text()).toBe('¥10.000000 CNY')
    await wrapper.get('input[aria-label="对外别名（试算）"]').setValue('missing')
    expect(wrapper.text()).toContain('未定价（不是免费）')
    expect(wrapper.find('[data-testid="quote-cost"]').exists()).toBe(false)
    expect(calls).toHaveLength(0)
  })
})

describe('independent loading, duplicate prevention and recoverable errors', () => {
  it('loads prices when omitted, disables editing while loading and retries a read error without a save', async () => {
    let finish!: (value: any) => void
    server(() => new Promise(resolve => { finish = resolve }))
    const wrapper = editor({ prices: undefined })
    expect(wrapper.text()).toContain('价格加载中')
    expect(wrapper.find('fieldset').attributes('disabled')).toBeDefined()
    await save(wrapper)
    expect(calls).toHaveLength(1)
    finish(response({ error: { message: '暂时无法读取价格' } }, 503))
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('暂时无法读取价格')
    server(() => response([{ model_id: 'upstream', version: 8, ...defaultMaps }]))
    await button(wrapper, '重试读取价格').trigger('click')
    await flushPromises()
    expect(value(wrapper.get('input[aria-label="版本"]'))).toBe('9')
    expect(calls.map(call => call.method)).toEqual(['GET', 'GET'])
  })

  it('rejects a known duplicate version locally and clears the error on a corrected version', async () => {
    const wrapper = editor({ prices: [{ model_id: 'upstream', version: 2, ...defaultMaps }] })
    await wrapper.get('input[aria-label="版本"]').setValue('2')
    await save(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toContain('相同模型与版本已存在')
    expect(calls).toHaveLength(0)
    await wrapper.get('input[aria-label="版本"]').setValue('3')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await save(wrapper)
    expect(calls).toHaveLength(1)
  })

  it('handles a server version conflict clearly, preserves the draft and allows explicit correction', async () => {
    server(() => response({ error: { message: 'duplicate key value violates unique constraint' } }, 409))
    const wrapper = editor()
    await save(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toContain('相同模型与版本已存在')
    expect(wrapper.get('[role="alert"]').text()).toContain('v2')
    expect(value(rate(wrapper, 'cost', 'input'))).toBe('1')
    await wrapper.get('input[aria-label="版本"]').setValue('2')
    server()
    await save(wrapper)
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(calls).toHaveLength(2)
  })

  it('guards multiple submits while pending and after success; cancel is an explicit event', async () => {
    let finish!: (value: any) => void
    server(() => new Promise(resolve => { finish = resolve }))
    const wrapper = editor()
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(calls).toHaveLength(1)
    expect(button(wrapper, '保存中…').attributes('disabled')).toBeDefined()
    finish(response({ id: 7, model_id: 'upstream', version: 1, ...defaultMaps }, 201))
    await flushPromises()
    await save(wrapper)
    expect(calls).toHaveLength(1)
    expect(wrapper.emitted('saved')).toHaveLength(1)
    await button(wrapper, '取消').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
  })

  it('releases busy state on network error without clearing fields or emitting success', async () => {
    server(() => Promise.reject(new Error('offline')))
    const wrapper = editor()
    await save(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toContain('网络')
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(button(wrapper, '保存新版本').attributes('disabled')).toBeUndefined()
    server()
    await save(wrapper)
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })
})
