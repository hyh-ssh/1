import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import App from './App.vue'
import { setCSRFToken } from './api'

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => (typeof body === 'string' ? body : JSON.stringify(body)) }
}
type Call = { url: string; method: string; body?: any }
let calls: Call[] = []
let storedPrices: any[] = []
const wrappers: VueWrapper[] = []
const admin = { id: 1, username: 'admin', display_name: '管理员', role: 'admin', password_login_enabled: true }

function stubAdmin(prices: any[] = []) {
  storedPrices = prices
  vi.stubGlobal('fetch', vi.fn((url: string, init: RequestInit = {}) => {
    const method = (init.method || 'GET').toUpperCase()
    calls.push({ url, method, body: init.body ? JSON.parse(String(init.body)) : undefined })
    if (url === '/api/auth/me') return Promise.resolve(response({ user: admin, csrf_token: 'test-csrf' }))
    if (url === '/api/admin/prices' && method === 'POST') return Promise.resolve(response({ id: 80, ...JSON.parse(String(init.body)) }, 201))
    if (url.startsWith('/api/admin/prices')) return Promise.resolve(response(storedPrices))
    if (url.startsWith('/api/admin/stats') || url.startsWith('/api/admin/runtime')) return Promise.resolve(response({}))
    if (url.startsWith('/api/admin/providers')) return Promise.resolve(response([]))
    if (url.startsWith('/api/admin/aliases')) return Promise.resolve(response([]))
    if (url === '/readyz') return Promise.resolve(response('ok'))
    return Promise.resolve(response([]))
  }))
}
async function openPrices() {
  const wrapper = mount(App)
  wrappers.push(wrapper)
  await flushPromises()
  const priceNav = wrapper.findAll('aside nav button').find(button => button.text().includes('价格'))!
  await priceNav.trigger('click'); await flushPromises()
  const newPrice = wrapper.findAll('button').find(button => button.text().includes('新建'))!
  await newPrice.trigger('click'); await flushPromises()
  await wrapper.get('input[aria-label="模型 ID"]').setValue('test-model')
  return wrapper
}
async function advancedJSON(wrapper: VueWrapper) {
  await wrapper.findAll('.price-editor button').find(button => button.text() === '高级 JSON')!.trigger('click')
}
function rateInputs(wrapper: VueWrapper) { return wrapper.findAll('.price-editor textarea') }
function freeButton(wrapper: VueWrapper) { return wrapper.findAll('.price-editor button').find(button => button.text() === '设为免费：成本和积分均为 0')! }
async function save(wrapper: VueWrapper) { await wrapper.get('.price-editor form').trigger('submit'); await flushPromises() }
function posts() { return calls.filter(call => call.url === '/api/admin/prices' && call.method === 'POST') }

beforeEach(() => {
  sessionStorage.clear(); setCSRFToken(''); calls = []
  window.history.replaceState({}, '', '#')
  vi.stubGlobal('scrollTo', vi.fn())
})
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  setCSRFToken(''); vi.unstubAllGlobals(); document.body.innerHTML = ''
})

describe('整壳价格页中的高级 JSON 免费操作', () => {
  it('保留默认双账费率，免费快捷操作只改草稿；显式保存后创建新版本', async () => {
    stubAdmin()
    const wrapper = await openPrices(); await advancedJSON(wrapper)
    const fields = rateInputs(wrapper)
    expect(JSON.parse(fields[0]!.element.textContent || (fields[0]!.element as HTMLTextAreaElement).value)).toEqual({ input: '0.000001', output: '0.000002' })
    expect(JSON.parse((fields[1]!.element as HTMLTextAreaElement).value)).toEqual({ input: '0.0002', output: '0.0004' })
    expect(wrapper.find('.price-editor').text()).toContain('免费调用仍记录用量')
    expect(wrapper.find('.price-editor').text()).toContain('请求数和 Token 限流及并发限制')
    expect(wrapper.find('.price-editor').text()).not.toContain('预算')

    await freeButton(wrapper).trigger('click'); await flushPromises()
    const expected = { input: '0', output: '0' }
    expect(JSON.parse((fields[0]!.element as HTMLTextAreaElement).value)).toEqual(expected)
    expect(JSON.parse((fields[1]!.element as HTMLTextAreaElement).value)).toEqual(expected)
    expect(posts()).toHaveLength(0)
    await save(wrapper)
    expect(posts()[0]!.body).toEqual({ model_id: 'test-model', version: 1, cost: expected, credits: expected })
  })

  it('合并两账所有已有和自定义计量项并全部置零，不自动提交', async () => {
    stubAdmin()
    const wrapper = await openPrices(); await advancedJSON(wrapper)
    const fields = rateInputs(wrapper)
    await fields[0]!.setValue('{"input":"0.01","cache_write":"2"}')
    await fields[1]!.setValue('{"output":"0.02","cache_read":"3"}')
    await freeButton(wrapper).trigger('click')
    const expected = { input: '0', output: '0', cache_write: '0', cache_read: '0' }
    expect(JSON.parse((fields[0]!.element as HTMLTextAreaElement).value)).toEqual(expected)
    expect(JSON.parse((fields[1]!.element as HTMLTextAreaElement).value)).toEqual(expected)
    expect(posts()).toHaveLength(0)
  })

  it.each([
    ['非法 JSON', '{broken'], ['数组', '[]'], ['null', 'null'], ['空字符串', ''],
    ['false 费率', '{"input":false}'], ['非数值费率', '{"input":"not-a-number"}'], ['null 费率', '{"input":null}'],
  ])('拒绝将%s修复成免费，且不能部分改写表单', async (_label, invalidCost) => {
    stubAdmin()
    const wrapper = await openPrices(); await advancedJSON(wrapper)
    const fields = rateInputs(wrapper)
    const validCredits = (fields[1]!.element as HTMLTextAreaElement).value
    await fields[0]!.setValue(invalidCost)
    const invalidBefore = (fields[0]!.element as HTMLTextAreaElement).value
    await freeButton(wrapper).trigger('click'); await flushPromises()
    expect((fields[0]!.element as HTMLTextAreaElement).value).toBe(invalidBefore)
    expect((fields[1]!.element as HTMLTextAreaElement).value).toBe(validCredits)
    expect(wrapper.find('.price-editor [role="alert"]').exists()).toBe(true)
    expect(posts()).toHaveLength(0)
  })

  it('积分 JSON 解析失败时成本表保持原样', async () => {
    stubAdmin()
    const wrapper = await openPrices(); await advancedJSON(wrapper)
    const fields = rateInputs(wrapper)
    await fields[0]!.setValue('{"input":"0.01","custom":"2"}')
    await fields[1]!.setValue('{broken')
    const before = fields.map(field => (field.element as HTMLTextAreaElement).value)
    await freeButton(wrapper).trigger('click'); await flushPromises()
    expect(fields.map(field => (field.element as HTMLTextAreaElement).value)).toEqual(before)
    expect(wrapper.find('.price-editor [role="alert"]').text()).toContain('积分 JSON 格式无效')
    expect(posts()).toHaveLength(0)
  })
})

describe('价格列表显式零费率分类与十进制边界', () => {
  it('仅完整双表全零版本标为免费，免积分、缺费率和非法数组不会误标', async () => {
    stubAdmin([
      { id: 1, model_id: 'free-model', version: 1, cost: { input: '0', output: 0 }, credits: { input: 0, output: '0' } },
      { id: 2, model_id: 'waived-credits', version: 1, cost: { input: '0.1' }, credits: { input: '0' } },
      { id: 3, model_id: 'missing-credit-meter', version: 1, cost: { input: '0' }, credits: {} },
      { id: 4, model_id: 'array-table', version: 1, cost: [0], credits: { input: 0 } },
    ])
    const wrapper = await openPrices()
    const rows = wrapper.findAll('.price-row')
    expect(rows).toHaveLength(4)
    expect(rows.map(row => row.find('.tag').text())).toEqual(['免费（成本和积分均为 0）', '免积分（仍记录上游成本）', '配置不完整', '配置不完整'])
  })

  it('将指数零、负号零和正号零判作精确零费率', async () => {
    stubAdmin([{ id: 6, model_id: 'decimal-zero-model', version: 1, cost: { input: '0e-1000', output: '-0.000' }, credits: { input: '+0', output: '0' } }])
    const wrapper = await openPrices()
    expect(wrapper.find('.price-row .tag').text()).toBe('免费（成本和积分均为 0）')
  })

  it('极小正费率即使显示/结算下溢为零也仍是付费，不标为免费或免积分', async () => {
    stubAdmin([
      { id: 5, model_id: 'tiny-paid-model', version: 1, cost: { input: '1e-1000' }, credits: { input: '1e-1000' } },
      { id: 7, model_id: 'tiny-credit-model', version: 1, cost: { input: '0.1' }, credits: { input: '1e-1000' } },
    ])
    const wrapper = await openPrices()
    const rows = wrapper.findAll('.price-row')
    expect(rows[0]!.find('.tag').text()).toBe('独立双账定价')
    expect(rows[0]!.text()).toContain('1e-1000')
    expect(rows[1]!.find('.tag').text()).toBe('独立双账定价')
    expect(rows.map(row => row.text()).join('\n')).not.toContain('免积分')
  })

  it('免费快捷操作拒绝负下溢费率，且不改写任何草稿字段', async () => {
    stubAdmin()
    const wrapper = await openPrices(); await advancedJSON(wrapper)
    const fields = rateInputs(wrapper)
    await fields[0]!.setValue('{"input":"-1e-1000","output":"0.000001"}')
    const before = fields.map(field => (field.element as HTMLTextAreaElement).value)
    await freeButton(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.find('.price-editor [role="alert"]').text()).toContain('非负数费率')
    expect(fields.map(field => (field.element as HTMLTextAreaElement).value)).toEqual(before)
    expect(posts()).toHaveLength(0)
  })

  it.each(['{"input":0.5}', '{"input":1e-1000}'])('拒绝 JSON 数值费率 %s，避免传输类型不匹配或精度丢失', async costJSON => {
    stubAdmin()
    const wrapper = await openPrices(); await advancedJSON(wrapper)
    const fields = rateInputs(wrapper)
    await fields[0]!.setValue(costJSON)
    const before = fields.map(field => (field.element as HTMLTextAreaElement).value)
    await freeButton(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.find('.price-editor [role="alert"]').text()).toContain('费率必须使用十进制字符串')
    expect(fields.map(field => (field.element as HTMLTextAreaElement).value)).toEqual(before)
    await save(wrapper)
    expect(wrapper.find('.price-editor [role="alert"]').text()).toContain('费率必须使用十进制字符串')
    expect(posts()).toHaveLength(0)
  })
})
