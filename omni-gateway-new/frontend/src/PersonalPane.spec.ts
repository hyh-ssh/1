import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import PersonalPane from './PersonalPane.vue'
import { setCSRFToken } from './api'
import { localInput } from './personalConsole'

type Call = { url: string; method: string; body: any; headers: Record<string, string> }
const member = { id: 2, username: 'alice', role: 'user', password_login_enabled: true, password_capabilities: { local_enabled: true, can_set_without_current: false, can_recover: false } }
const sso = { ...member, password_login_enabled: false, password_capabilities: { local_enabled: false, can_set_without_current: true, can_recover: false } }
const initialKey = { id: 5, name: 'client-key', key_prefix: 'sk-test', key_suffix: '1234', active: true, model_allow: [] as string[], expires_at: null as string | null, last_used_at: '2026-10-02T05:00:00Z' }
const oneTimeSecret = 'test-only-one-time-secret-never-persist'
const chat = { id: 'alias-chat', protocols: ['chat_completions', 'responses'], capabilities: { streaming: true, tools: false }, pricing_status: 'free', ready: true, reasons: [] }
const embedding = { id: 'alias-embed', protocols: ['embeddings'], capabilities: null, pricing_status: 'unpriced', ready: true, reasons: [] }
const initialRecord = { id: 'stored-request-actual-001', model_alias: 'alias-chat', api_key_id: 5, status: 'success', status_code: 200, created_at: '2026-10-03T08:01:02+08:00', usage: { meters: { total: 2345, input: 2000, output: 345 }, estimated: false }, cost: '0.000000', credits: '0.000', total_latency_ms: 120, settlement_status: 'free' }
let calls: Call[]
let wrappers: VueWrapper<any>[]
let service: { user: any; keys: any[]; records: any[]; access: Record<number, any> }

function response(body: any, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(body) }
}
function deferred<T = any>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: any) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function clipboard(value: any) { Object.defineProperty(navigator, 'clipboard', { configurable: true, value }) }
function server(override: (call: Call) => any = () => undefined) {
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const call: Call = { url, method: init.method || 'GET', body: init.body ? JSON.parse(String(init.body)) : undefined, headers: (init.headers || {}) as Record<string, string> }
    calls.push(call)
    const custom = override(call)
    if (custom !== undefined) return custom
    const parsed = new URL(url, 'http://localhost')
    if (parsed.pathname === '/api/self/summary') return response({ user: service.user, usage: { requests: 8, success: 8, errors: 0, total_tokens: 2345, cost: '0.000000', credits: '0.000', success_rate: 100, avg_latency_ms: 120 }, limits: [{ id: 1, scope: 'user', rpm: 60, tpm: 9000, concurrency: 2, active: true }], budgets: [{ cost_limit: '9999999', secret_marker: 'removed-budget-marker' }] })
    if (parsed.pathname === '/api/self/api-keys' && call.method === 'GET') return response(service.keys)
    if (parsed.pathname === '/api/self/api-keys' && call.method === 'POST') {
      const key = { ...initialKey, ...call.body, id: 9, key_suffix: '9999', last_used_at: '2026-10-03T02:00:00Z' }
      service.keys.push(key)
      return response({ key, secret: oneTimeSecret }, 201)
    }
    if (/^\/api\/self\/api-keys\/\d+$/.test(parsed.pathname)) {
      const id = Number(parsed.pathname.split('/').pop()), key = service.keys.find((row) => row.id === id)
      if (call.method === 'PATCH') { Object.assign(key, call.body); return response(key) }
      if (call.method === 'DELETE') { service.keys = service.keys.filter((row) => row.id !== id); return response(null, 204) }
    }
    if (parsed.pathname === '/api/self/access') {
      const id = Number(parsed.searchParams.get('api_key_id')), key = service.keys.find((row) => row.id === id)
      if (service.access[id]) return response(service.access[id])
      return response({ api_key_id: id, key_status: key?.active === false ? 'inactive' : key?.expires_at && Date.parse(key.expires_at) < Date.now() ? 'expired' : 'active', first_success_at: id === 5 ? '2026-10-01T01:00:00Z' : null, models: [chat, embedding].filter((model) => !key?.model_allow?.length || key.model_allow.includes(model.id)) })
    }
    if (parsed.pathname === '/api/self/requests') return response(service.records)
    if (parsed.pathname === '/api/self/password') {
      service.user = { ...service.user, password_login_enabled: true, password_capabilities: { local_enabled: true, can_set_without_current: false, can_recover: false } }
      return response(null, 204)
    }
    throw new Error(`Unexpected endpoint: ${call.method} ${url}`)
  }))
}
async function pane(view: 'account' | 'myKeys' | 'myRequests' = 'myKeys', user: any = member, context?: Record<string, string>) {
  const wrapper = mount(PersonalPane, { props: { view, user, ...(context ? { context } : {}) }, attachTo: document.body })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
function button(wrapper: any, text: string) { return wrapper.findAll('button').find((node: any) => node.text() === text)! }
function writes() { return calls.filter((call) => call.method !== 'GET') }
function last<T>(values: readonly T[] | undefined): T { return values![values!.length - 1]! }
function lastRequest() { return new URL(last(calls.filter((call) => call.url.startsWith('/api/self/requests'))).url, 'http://localhost') }
async function openCreate(wrapper: VueWrapper<any>, name = 'new-client') {
  await wrapper.get('[data-testid="new-key"]').trigger('click'); await flushPromises()
  await wrapper.get('[data-testid="key-name"]').setValue(name)
}
async function create(wrapper: VueWrapper<any>) {
  await openCreate(wrapper)
  await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
}
async function fillPassword(wrapper: VueWrapper<any>, next = 'NextPassword!42', confirmation = next) {
  await wrapper.get('[data-testid="new-password"]').setValue(next)
  await wrapper.get('[data-testid="confirm-password"]').setValue(confirmation)
}

beforeEach(() => {
  calls = []; wrappers = []
  service = { user: { ...member }, keys: [{ ...initialKey, model_allow: [] }], records: [{ ...initialRecord }], access: {} }
  sessionStorage.clear(); localStorage.clear(); setCSRFToken('test-session-csrf')
  clipboard(undefined); (document as any).execCommand = undefined
  server()
})
afterEach(() => {
  for (const wrapper of wrappers) wrapper.unmount()
  setCSRFToken(''); vi.restoreAllMocks(); vi.unstubAllGlobals()
  clipboard(undefined); (document as any).execCommand = undefined
  document.body.innerHTML = ''
})

describe('个人密钥：本人签发、生命周期和一次性明文', () => {
  it.each(['user', 'admin'])('%s 角色使用相同的 self API、掩码和模型选项，没有归属选择或管理请求', async (role) => {
    const wrapper = await pane('myKeys', { ...member, role })
    expect(wrapper.text()).toContain('sk-test…1234')
    expect(wrapper.text()).toContain('空白名单：全部启用的模型别名')
    expect(wrapper.text()).toContain('最后使用（不代表成功）')
    expect(wrapper.text()).toContain('2026-10-02 05:00:00 UTC')
    await openCreate(wrapper)
    expect(wrapper.get('[role="dialog"]').text()).toContain('归属当前会话账号')
    expect(wrapper.get('[role="dialog"]').text()).toContain('显式空白名单 = 全部启用的模型别名')
    expect(wrapper.find('input[name="user_id"]').exists()).toBe(false)
    expect(wrapper.findAll('input[type="checkbox"]').map((input) => (input.element as HTMLInputElement).value)).toEqual(['alias-chat', 'alias-embed'])
    await wrapper.get('input[value="alias-embed"]').setValue(true)
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    expect(writes()[0].body).toEqual({ name: 'new-client', model_allow: ['alias-embed'] })
    expect(writes()[0].headers['X-CSRF-Token']).toBe('test-session-csrf')
    expect(calls.every((call) => call.url.startsWith('/api/self/'))).toBe(true)
    expect(calls.some((call) => call.url.includes('user_id'))).toBe(false)
  })

  it('新建显式空白名单，不发送空到期字符串；明文只存在于可复制确认保存的弹窗', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined); clipboard({ writeText })
    const wrapper = await pane()
    await create(wrapper)
    expect(writes()[0].body).toEqual({ name: 'new-client', model_allow: [] })
    expect((wrapper.get('[data-testid="one-time-secret"]').element as HTMLTextAreaElement).value).toBe(oneTimeSecret)
    expect(wrapper.find('.notice').exists()).toBe(false)
    expect(wrapper.find('.key-list').text()).not.toContain(oneTimeSecret)
    expect(JSON.stringify(localStorage)).not.toContain(oneTimeSecret)
    expect(JSON.stringify(sessionStorage)).not.toContain(oneTimeSecret)
    await button(wrapper, '复制密钥').trigger('click'); await flushPromises()
    expect(writeText).toHaveBeenCalledWith(oneTimeSecret)
    expect(wrapper.get('[role="dialog"]').text()).toContain('已复制，请确认已经安全保存')
    await button(wrapper, '我已安全保存').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.html()).not.toContain(oneTimeSecret)
    await button(wrapper, '刷新密钥').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="one-time-secret"]').exists()).toBe(false)
  })

  it('clipboard 拒绝时使用临时复制回退并移除；失败可选中明文手动保存', async () => {
    clipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) })
    const exec = vi.fn(() => true); (document as any).execCommand = exec
    const wrapper = await pane(); await create(wrapper)
    await button(wrapper, '复制密钥').trigger('click'); await flushPromises()
    expect(exec).toHaveBeenCalledWith('copy')
    expect(document.querySelectorAll('body > textarea')).toHaveLength(0)
    expect(wrapper.get('[role="dialog"]').text()).toContain('已复制')
    exec.mockReturnValue(false)
    await button(wrapper, '复制密钥').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('手动复制')
    expect((wrapper.get('[data-testid="one-time-secret"]').element as HTMLTextAreaElement).readOnly).toBe(true)
  })

  it('Escape 需要二次确认丢弃明文；Tab 保持弹窗焦点，关闭返回原按钮', async () => {
    const wrapper = await pane()
    const trigger = wrapper.get('[data-testid="new-key"]'); (trigger.element as HTMLElement).focus()
    await create(wrapper)
    const modal = wrapper.get('[role="dialog"]')
    const controls = modal.findAll('button, textarea')
    const finalControl = last(controls); (finalControl.element as HTMLElement).focus()
    await finalControl.trigger('keydown', { key: 'Tab' })
    expect(document.activeElement).toBe(controls[0].element)
    await modal.trigger('keydown', { key: 'Escape' })
    expect(wrapper.get('[role="dialog"]').text()).toContain('清除后无法再次显示')
    await button(wrapper, '继续保存').trigger('click')
    expect(wrapper.find('[data-testid="one-time-secret"]').exists()).toBe(true)
    await wrapper.get('[role="dialog"]').trigger('keydown', { key: 'Escape' })
    await button(wrapper, '放弃并清除明文').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.html()).not.toContain(oneTimeSecret)
    expect(document.activeElement).toBe(trigger.element)
  })

  it('创建和编辑校验未来时间，提交 UTC；编辑可清除到期和白名单，不需要原密钥', async () => {
    service.keys[0].model_allow = ['alias-chat', 'legacy-alias']
    service.keys[0].expires_at = '2099-01-01T00:00:00Z'
    const wrapper = await pane()
    await button(wrapper.get('[data-key-id="5"]'), '编辑').trigger('click'); await flushPromises()
    expect((wrapper.get('[data-testid="key-expiry"]').element as HTMLInputElement).value).toBe(localInput('2099-01-01T00:00:00Z'))
    expect(wrapper.get('[role="dialog"]').text()).toContain('legacy-alias')
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)
    await wrapper.get('[data-testid="key-name"]').setValue('renamed')
    await wrapper.get('[data-testid="key-expiry"]').setValue('2000-01-01T00:00')
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('过期时间必须晚于当前时间')
    expect(writes()).toHaveLength(0)
    const future = '2099-05-06T12:30'
    await wrapper.get('[data-testid="key-expiry"]').setValue(future)
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    expect(writes()[0].body).toEqual({ name: 'renamed', expires_at: new Date(future).toISOString(), model_allow: ['alias-chat', 'legacy-alias'] })
    await button(wrapper.get('[data-key-id="5"]'), '编辑').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="key-expiry"]').setValue('')
    await button(wrapper, '清空白名单（全部启用别名）').trigger('click')
    // Unknown existing aliases remain available to reselect instead of vanishing.
    expect(wrapper.find('input[value="legacy-alias"]').exists()).toBe(true)
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    expect(writes()[1].body).toEqual({ name: 'renamed', expires_at: null, model_allow: [] })
    expect(wrapper.get('[data-key-id="5"]').text()).toContain('永不过期')
  })

  it('创建到期时间也按本机时间转换为 UTC，无归属字段', async () => {
    const wrapper = await pane(); await openCreate(wrapper)
    await wrapper.get('[data-testid="key-expiry"]').setValue('2099-02-03T12:34')
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    expect(writes()[0].body.expires_at).toBe(new Date('2099-02-03T12:34').toISOString())
    expect(writes()[0].body).not.toHaveProperty('user_id')
  })

  it('停用、启用和删除都由本人明确确认，无自动轮换操作', async () => {
    const wrapper = await pane()
    await button(wrapper.get('[data-key-id="5"]'), '停用').trigger('click')
    expect(writes()).toHaveLength(0)
    expect(wrapper.get('[role="dialog"]').text()).toContain('人工确认新密钥的成功记录')
    await wrapper.get('[data-testid="confirm-key-action"]').trigger('click'); await flushPromises()
    expect(writes()[0]).toMatchObject({ method: 'PATCH', url: '/api/self/api-keys/5', body: { active: false } })
    expect(wrapper.get('[data-key-id="5"]').text()).toContain('已停用')
    await button(wrapper.get('[data-key-id="5"]'), '启用').trigger('click')
    await wrapper.get('[data-testid="confirm-key-action"]').trigger('click'); await flushPromises()
    expect(writes()[1].body).toEqual({ active: true })
    await button(wrapper.get('[data-key-id="5"]'), '删除').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('删除不可恢复')
    await wrapper.get('[data-testid="confirm-key-action"]').trigger('click'); await flushPromises()
    expect(writes()[2]).toMatchObject({ method: 'DELETE', url: '/api/self/api-keys/5' })
    expect(wrapper.find('[data-key-id="5"]').exists()).toBe(false)
  })

  it('轮换先手动建新，验证基于新密钥实际 first_success_at，不用最后使用、不会停旧', async () => {
    const wrapper = await pane()
    await button(wrapper, '新建轮换密钥').trigger('click'); await flushPromises()
    expect(writes()).toHaveLength(0)
    await wrapper.get('[data-testid="key-name"]').setValue('rotation-new')
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    await button(wrapper, '我已安全保存').trigger('click')
    expect((wrapper.get('[data-testid="access-key"]').element as HTMLSelectElement).value).toBe('9')
    expect(wrapper.get('[data-testid="first-success"]').text()).toContain('尚无此密钥的成功记录')
    expect(wrapper.get('[data-key-id="9"]').text()).toContain('2026-10-03 02:00:00 UTC')
    expect(service.keys.find((key) => key.id === 5).active).toBe(true)
    expect(writes()).toHaveLength(1)
    service.access[9] = { api_key_id: 9, key_status: 'active', first_success_at: '2026-10-03T03:00:00Z', models: [chat] }
    await button(wrapper, '刷新接入状态').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="first-success"]').text()).toContain('首次成功：2026-10-03 03:00:00 UTC')
    await button(wrapper, '查看此密钥成功记录').trigger('click')
    expect(last(wrapper.emitted('navigate'))).toEqual([{ view: 'myRequests', context: { page: '1', api_key_id: '9', status: 'success' } }])
    expect(writes()).toHaveLength(1)
  })

  it('签发、编辑与停用重复提交均被锁定，关闭待签发弹窗不会泄漏迟到明文', async () => {
    const pending = deferred(); server((call) => call.method === 'POST' && call.url === '/api/self/api-keys' ? pending.promise : undefined)
    const wrapper = await pane(); await openCreate(wrapper)
    await wrapper.get('[data-testid="key-form"]').trigger('submit')
    await wrapper.get('[data-testid="key-form"]').trigger('submit')
    expect(writes()).toHaveLength(1)
    await wrapper.get('[role="dialog"]').trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    pending.resolve(response({ key: { id: 9 }, secret: oneTimeSecret }, 201)); await flushPromises()
    expect(wrapper.find('[data-testid="one-time-secret"]').exists()).toBe(false)
    const mutation = deferred(); server((call) => call.method === 'PATCH' ? mutation.promise : undefined)
    await button(wrapper.get('[data-key-id="5"]'), '停用').trigger('click')
    await wrapper.get('[data-testid="confirm-key-action"]').trigger('click')
    await wrapper.get('[data-testid="confirm-key-action"]').trigger('click')
    expect(writes().filter((call) => call.method === 'PATCH')).toHaveLength(1)
    mutation.resolve(response({ ...initialKey, active: false })); await flushPromises()
  })
})

describe('快速接入：正确地址、类型化能力和安全示例', () => {
  it('Base URL 和完整 URL 分开；复制仅元数据，curl/Python 引用环境变量不包含明文或上游', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined); clipboard({ writeText })
    service.keys[0].secret = 'must-not-display'; service.keys[0].key_hash = 'hash-not-display'
    service.access[5] = { api_key_id: 5, key_status: 'active', first_success_at: '2026-10-01T01:00:00Z', models: [{ ...chat, provider_model: 'private-upstream-model', base_url: 'https://private-upstream.example', api_key: 'upstream-secret' }] }
    const wrapper = await pane()
    expect((wrapper.get('input[aria-label="Base URL"]').element as HTMLInputElement).value).toBe(`${location.origin}/v1`)
    expect(wrapper.text()).toContain(`${location.origin}/v1/chat/completions`)
    expect(wrapper.get('[data-testid="curl-example"]').text()).toContain('${GATEWAY_API_KEY}')
    expect(wrapper.get('[data-testid="python-example"]').text()).toContain('os.environ["GATEWAY_API_KEY"]')
    expect(wrapper.get('[data-testid="python-example"]').text()).toContain('base_url=os.environ["OPENAI_BASE_URL"]')
    for (const value of ['must-not-display', 'hash-not-display', 'private-upstream-model', 'private-upstream.example', 'upstream-secret', '/v1/v1']) expect(wrapper.html()).not.toContain(value)
    await button(wrapper, '复制 Base URL').trigger('click')
    await button(wrapper, '复制 curl 示例').trigger('click')
    await button(wrapper, '复制 Python 示例').trigger('click'); await flushPromises()
    expect(writeText.mock.calls[0][0]).toBe(`${location.origin}/v1`)
    expect(calls.every((call) => call.method === 'GET' && call.url.startsWith('/api/self/'))).toBe(true)
    expect(calls.filter((call) => call.url.startsWith('/api/self/access'))).toHaveLength(1)
  })

  it('选择本人密钥和别名，每次能力查询限定所选密钥，无协议的别名不默认为 chat', async () => {
    service.keys.push({ ...initialKey, id: 6, name: 'scoped-key', model_allow: ['unknown-alias'] })
    service.access[6] = { api_key_id: 6, key_status: 'active', first_success_at: null, models: [{ id: 'unknown-alias', protocols: ['future_api'], capabilities: null, ready: true, pricing_status: 'mixed', reasons: [] }] }
    const wrapper = await pane()
    await wrapper.get('[data-testid="access-alias"]').setValue('alias-embed')
    expect(wrapper.text()).toContain('缺少价格（不等同免费）')
    expect(wrapper.get('[data-testid="curl-example"]').text()).toContain('/embeddings')
    await wrapper.get('[data-testid="access-key"]').setValue('6'); await flushPromises()
    expect(last(calls).url).toBe('/api/self/access?api_key_id=6')
    expect(wrapper.get('[data-testid="access-alias"]').text()).toBe('unknown-alias')
    expect(wrapper.text()).toContain('能力未知')
    expect(wrapper.text()).toContain('没有可确认的已知协议')
    expect(wrapper.find('[data-testid="curl-example"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="access-protocol"]').exists()).toBe(false)
  })

  it('只按布尔值展示已确认支持/不支持，字符串和数字能力不冒充布尔能力', async () => {
    service.access[5] = { api_key_id: 5, key_status: 'active', models: [{ ...chat, capabilities: { streaming: true, tools: false, vision: 'true', json_mode: 1 } }] }
    const wrapper = await pane()
    expect(wrapper.text()).toContain('流式输出：已确认支持')
    expect(wrapper.text()).toContain('工具调用：已确认不支持')
    expect(wrapper.text()).not.toContain('图像输入：已确认支持')
    expect(wrapper.text()).not.toContain('JSON 输出：已确认支持')
  })

  it.each([['chat_completions', '/chat/completions', 'client.chat.completions.create'], ['responses', '/responses', 'client.responses.create'], ['messages', '/messages', 'urllib.request.Request'], ['embeddings', '/embeddings', 'client.embeddings.create'], ['images_generations', '/images/generations', 'client.images.generate']])('%s 只按服务器已确认协议生成对应示例', async (protocol, path, method) => {
    service.access[5] = { api_key_id: 5, key_status: 'active', first_success_at: null, models: [{ ...chat, protocols: [protocol] }] }
    const wrapper = await pane()
    expect(wrapper.get('[data-testid="curl-example"]').text()).toContain(path)
    expect(wrapper.get('[data-testid="python-example"]').text()).toContain(method)
    if (protocol === 'messages') {
      expect(wrapper.text()).toContain('不是 OpenAI SDK 接口')
      expect(wrapper.get('[data-testid="curl-example"]').text()).toContain('x-api-key: ${GATEWAY_API_KEY}')
    }
    expect(writes()).toHaveLength(0)
  })

  it.each(['inactive', 'expired'])('%s 密钥明确不能调用，不生成可执行示例且不自动启用/续期', async (status) => {
    service.keys[0].active = status !== 'inactive'
    service.keys[0].expires_at = status === 'expired' ? '2000-01-01T00:00:00Z' : null
    const wrapper = await pane()
    expect(wrapper.text()).toContain(status === 'inactive' ? '所选密钥已停用' : '所选密钥已过期')
    expect(wrapper.find('[data-testid="curl-example"]').exists()).toBe(false)
    expect(wrapper.get('[data-key-id="5"]').text()).toContain(status === 'inactive' ? '已停用' : '已过期')
    expect(writes()).toHaveLength(0)
  })

  it('响应密钥 ID 不匹配、模型未就绪都不提供可调用示例', async () => {
    service.access[5] = { api_key_id: 999, key_status: 'active', models: [chat] }
    const wrapper = await pane()
    expect(wrapper.text()).toContain('接入能力响应无效')
    expect(wrapper.find('[data-testid="curl-example"]').exists()).toBe(false)
    service.access[5] = { api_key_id: 5, key_status: 'active', models: [{ ...chat, ready: false, reasons: ['private raw error must not echo'] }] }
    await button(wrapper, '刷新接入状态').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('模型尚未就绪')
    expect(wrapper.text()).not.toContain('private raw error')
    expect(wrapper.find('[data-testid="curl-example"]').exists()).toBe(false)
  })
})

describe('本人请求：结算、安全诊断、分页和 URL 上下文', () => {
  it('免费、免积分、缺价和结算失败独立表达，保留成功结果、实际/估算 Token 和零金额', async () => {
    service.records = ['free', 'credits_free', 'unpriced', 'failed', undefined].map((status, index) => ({ ...initialRecord, id: `stored-${index}`, settlement_status: status, usage: { ...initialRecord.usage, estimated: index === 1 } }))
    const wrapper = await pane('myRequests')
    const rows = wrapper.findAll('.request-row')
    expect(rows[0].text()).toContain('免费（显式零费率）')
    expect(rows[1].text()).toContain('免积分（成本仍独立核算）')
    expect(rows[2].text()).toContain('缺价（不等同免费）')
    expect(rows[3].text()).toContain('结算失败')
    expect(rows[4].text()).toContain('结算状态未知')
    expect(rows[4].text()).not.toContain('免费')
    for (const row of rows) {
      expect(row.text()).toContain('成功'); expect(row.text()).toContain('2,345 Token')
      expect(row.text()).toContain('¥0.000000 CNY'); expect(row.text()).toContain('0.000 积分')
      expect(row.text()).toContain('2026-10-03 00:01:02 UTC')
    }
    expect(rows[0].text()).toContain('实际用量'); expect(rows[1].text()).toContain('估算用量')
  })

  it.each([[429, '限流', '降低并发'], [504, '超时', '稍后重试'], [401, '鉴权失败', '网关密钥'], [403, '访问受限', '密钥白名单'], [501, '参数或协议不兼容', '快速接入'], [502, '服务暂不可用', '管理员'], [0, '原因未确认', '实际请求 ID']])('HTTP %i 显示安全中文类型和建议，不展示原始上游摘要/正文', async (status, type, next) => {
    service.records = [{ ...initialRecord, status: 'error', status_code: status, error_summary: 'sensitive-upstream-body', request_body: 'sensitive-request-body', response_body: 'sensitive-response-body', error_type: 'unrecognized' }]
    const wrapper = await pane('myRequests')
    expect(wrapper.get('.diagnosis').text()).toContain(type)
    expect(wrapper.get('.diagnosis').text()).toContain(next)
    expect(wrapper.get('.request-row').text()).toContain('失败')
    for (const value of ['sensitive-upstream-body', 'sensitive-request-body', 'sensitive-response-body']) expect(wrapper.html()).not.toContain(value)
    expect(calls.every((call) => call.url.startsWith('/api/self/'))).toBe(true)
  })

  it('复制实际保存的请求 ID，不用 trace ID；缺失 ID 不伪造复制入口', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined); clipboard({ writeText })
    service.records = [{ ...initialRecord, trace_id: 'untrusted-trace-id' }, { ...initialRecord, id: undefined }]
    const wrapper = await pane('myRequests')
    expect(wrapper.findAll('button').filter((node) => node.text() === '复制请求 ID')).toHaveLength(1)
    await button(wrapper, '复制请求 ID').trigger('click'); await flushPromises()
    expect(writeText).toHaveBeenCalledWith(initialRecord.id)
    expect(wrapper.text()).toContain('未记录请求 ID')
    expect(wrapper.text()).not.toContain('untrusted-trace-id')
  })

  it('从 context 恢复全部安全筛选和页码，忽略其他字段，应用筛选重置页码并发完整 context', async () => {
    service.records = Array.from({ length: 50 }, (_, i) => ({ ...initialRecord, id: `stored-${i}` }))
    const wrapper = await pane('myRequests', member, { page: '3', status: 'error', from: '2026-10-01T08:00:00+08:00', to: '2026-10-03T00:00:00Z', api_key_id: '5', request_id: 'stored-filter-id', user_id: '999', secret: 'never-in-query', detail: 'not-supported' })
    expect(lastRequest().searchParams.get('offset')).toBe('100')
    expect(lastRequest().searchParams.get('from')).toBe('2026-10-01T00:00:00.000Z')
    expect(lastRequest().searchParams.get('request_id')).toBe('stored-filter-id')
    expect(lastRequest().searchParams.has('user_id')).toBe(false)
    expect(lastRequest().searchParams.has('secret')).toBe(false)
    expect((wrapper.get('[data-testid="filter-from"]').element as HTMLInputElement).value).toBe('2026-10-01T00:00')
    await wrapper.get('[data-testid="next-page"]').trigger('click'); await flushPromises()
    const pageEvent = last(wrapper.emitted('context-change'))[0] as Record<string, string>
    expect(pageEvent).toEqual({ page: '4', status: 'error', from: '2026-10-01T00:00:00.000Z', to: '2026-10-03T00:00:00.000Z', api_key_id: '5', request_id: 'stored-filter-id' })
    expect(lastRequest().searchParams.get('offset')).toBe('150')
    await wrapper.get('[data-testid="filter-status"]').setValue('success')
    await wrapper.get('[data-testid="filter-from"]').setValue('2026-10-02T01:23')
    await wrapper.get('[data-testid="filter-request-id"]').setValue('new-exact-id')
    await wrapper.get('[data-testid="request-filters"]').trigger('submit'); await flushPromises()
    expect(last(wrapper.emitted('context-change'))[0]).toEqual({ ...pageEvent, page: '1', status: 'success', from: '2026-10-02T01:23:00.000Z', request_id: 'new-exact-id' })
    expect(lastRequest().searchParams.get('offset')).toBe('0')
    const reads = calls.filter((call) => call.url.startsWith('/api/self/requests')).length
    await wrapper.setProps({ context: last(wrapper.emitted('context-change'))[0] as Record<string, string> }); await flushPromises()
    expect(calls.filter((call) => call.url.startsWith('/api/self/requests'))).toHaveLength(reads)
    await wrapper.setProps({ context: { page: '2', status: 'error' } }); await flushPromises()
    expect(lastRequest().searchParams.get('offset')).toBe('50')
    expect((wrapper.get('[data-testid="filter-request-id"]').element as HTMLInputElement).value).toBe('')
  })

  it('时间边界错误不查询，空页可回前页；少于 50 条禁止下一页，清筛选移除旧上下文', async () => {
    const wrapper = await pane('myRequests', member, { page: '2', status: 'error' })
    expect(wrapper.get('[data-testid="next-page"]').attributes('disabled')).toBeDefined()
    const count = calls.length
    await wrapper.get('[data-testid="filter-from"]').setValue('2026-10-03T00:00')
    await wrapper.get('[data-testid="filter-to"]').setValue('2026-10-01T00:00')
    await wrapper.get('[data-testid="request-filters"]').trigger('submit')
    expect(wrapper.text()).toContain('起始时间不能晚于结束时间'); expect(calls).toHaveLength(count)
    service.records = []
    await button(wrapper, '清除筛选').trigger('click'); await flushPromises()
    expect(last(wrapper.emitted('context-change'))).toEqual([{ page: '1' }])
    expect(wrapper.text()).toContain('没有请求记录')
    expect(wrapper.get('[data-testid="previous-page"]').attributes('disabled')).toBeDefined()
  })

  it('读取中不误报空，错误保留实际 code/request_id；缺失时不创造诊断 ID，读取重试不调用模型', async () => {
    const pending = deferred(); server((call) => call.url.startsWith('/api/self/requests') ? pending.promise : undefined)
    const wrapper = await pane('myRequests')
    expect(wrapper.text()).toContain('正在加载本人请求记录')
    expect(wrapper.text()).not.toContain('没有请求记录')
    pending.resolve(response({ error: { message: 'raw sensitive data must not echo', code: 'safe_real_code', request_id: 'real-failure-id' } }, 502)); await flushPromises()
    expect(wrapper.text()).toContain('code: safe_real_code'); expect(wrapper.text()).toContain('请求 ID: real-failure-id')
    expect(wrapper.text()).not.toContain('raw sensitive data')
    server((call) => call.url.startsWith('/api/self/requests') ? response({ error: { message: 'unknown' } }, 503) : undefined)
    await button(wrapper, '重试读取').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('HTTP 503')
    expect(wrapper.text()).not.toContain('code:'); expect(wrapper.text()).not.toContain('请求 ID: real-failure-id')
    expect(writes()).toHaveLength(0)
  })
})

describe('账号密码：能力、SSO 和原密码边界', () => {
  it('账号 summary 忽略旧预算，仍展示本人用量及限流，导航使用父级事件', async () => {
    const wrapper = await pane('account')
    expect(wrapper.text()).toContain('账号中心 · alice')
    expect(wrapper.text()).toContain('2,345 Token'); expect(wrapper.text()).toContain('¥0.000000')
    expect(wrapper.text()).toContain('RPM 60 · TPM 9000 · 并发 2')
    for (const value of ['预算', '9999999', 'removed-budget-marker']) expect(wrapper.text()).not.toContain(value)
    await button(wrapper, '我的密钥与快速接入').trigger('click')
    expect(last(wrapper.emitted('navigate'))).toEqual([{ view: 'myKeys' }])
    expect(calls.every((call) => call.url.startsWith('/api/self/'))).toBe(true)
  })

  it('SSO-only 由能力声明免原密码，解释开启本地 + 保留 SSO，成功清空并刷新能力', async () => {
    service.user = { ...sso }
    const wrapper = await pane('account', sso)
    expect(wrapper.find('[data-testid="current-password"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('将开启本地密码登录，SSO 仍可使用且原绑定不变')
    expect(wrapper.text()).toContain('其他设备 / 会话将失效')
    await fillPassword(wrapper)
    await wrapper.get('[data-testid="password-form"]').trigger('submit'); await flushPromises()
    expect(writes()[0]).toMatchObject({ url: '/api/self/password', body: { current_password: '', new_password: 'NextPassword!42' } })
    expect(writes()[0].headers['X-CSRF-Token']).toBe('test-session-csrf')
    expect(wrapper.emitted('user-refresh')).toEqual([[]])
    expect(wrapper.find('[data-testid="current-password"]').exists()).toBe(true)
    expect((wrapper.get('[data-testid="new-password"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).toContain('SSO 绑定保持不变')
  })

  it('已有本地密码必须验证原密码，强度/一致/不能相同均阻止提交', async () => {
    const wrapper = await pane('account')
    expect(wrapper.find('[data-testid="current-password"]').exists()).toBe(true)
    await fillPassword(wrapper)
    await wrapper.get('[data-testid="password-form"]').trigger('submit')
    expect(writes()).toHaveLength(0)
    await wrapper.get('[data-testid="current-password"]').setValue('CurrentPassword!42')
    await fillPassword(wrapper, 'short')
    expect(wrapper.get('[data-testid="password-form"] button.primary').attributes('disabled')).toBeDefined()
    await fillPassword(wrapper, 'NextPassword!42', 'different-confirm')
    await wrapper.get('[data-testid="password-form"]').trigger('submit')
    expect(writes()).toHaveLength(0)
    await fillPassword(wrapper, 'CurrentPassword!42')
    await wrapper.get('[data-testid="password-form"]').trigger('submit')
    expect(writes()).toHaveLength(0)
    await fillPassword(wrapper)
    await wrapper.get('[data-testid="password-form"]').trigger('submit'); await flushPromises()
    expect(writes()[0].body.current_password).toBe('CurrentPassword!42')
    expect(wrapper.text()).toContain('当前会话保留')
  })

  it.each([{}, { password_login_enabled: false }, { password_capabilities: { local_enabled: 'false', can_set_without_current: 'true' } }])('缺失或无法确认能力禁用提交，不能按角色或假布尔值推断免原密码', async (caps) => {
    const unknown = { id: 2, username: 'alice', role: 'admin', ...caps }; service.user = unknown
    const wrapper = await pane('account', unknown)
    expect(wrapper.text()).toContain('密码能力未知')
    expect(wrapper.get('[data-testid="new-password"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="password-form"]').trigger('submit'); expect(writes()).toHaveLength(0)
  })

  it('能力可从本人 summary 获取，已启用本地时即使错误地允许免原密码也仍要求当前密码', async () => {
    service.user = { ...sso }
    const wrapper = await pane('account', { id: 2, username: 'alice', role: 'user' })
    expect(wrapper.text()).toContain('设置本地密码')
    service.user = { ...sso, password_capabilities: { local_enabled: true, can_set_without_current: true } }
    await button(wrapper, '刷新账号信息').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="current-password"]').exists()).toBe(true)
  })

  it('密码 POST 不重复提交，失败不保留密码，不报告刷新成功', async () => {
    const pending = deferred(); server((call) => call.url === '/api/self/password' ? pending.promise : undefined)
    const wrapper = await pane('account')
    await wrapper.get('[data-testid="current-password"]').setValue('CurrentPassword!42'); await fillPassword(wrapper)
    await wrapper.get('[data-testid="password-form"]').trigger('submit')
    await wrapper.get('[data-testid="password-form"]').trigger('submit')
    expect(writes()).toHaveLength(1)
    pending.resolve(response({ error: { code: 'invalid_current_password', message: 'current password is incorrect', request_id: 'actual-password-error' } }, 403)); await flushPromises()
    expect(wrapper.text()).toContain('当前密码不正确')
    expect(wrapper.text()).toContain('actual-password-error')
    expect(wrapper.emitted('user-refresh')).toBeUndefined()
    expect(wrapper.findAll('input[type="password"]').every((node) => (node.element as HTMLInputElement).value === '')).toBe(true)
  })
})

describe('敏感数据清理与异步生命周期', () => {
  it.each(['view', 'user', 'unmount'])('一次性明文已显示后 %s 清理，返回原视图也不回显', async (change) => {
    const wrapper = await pane(); await create(wrapper)
    expect(wrapper.find('[data-testid="one-time-secret"]').exists()).toBe(true)
    if (change === 'unmount') { wrapper.unmount(); expect(document.body.textContent).not.toContain(oneTimeSecret); return }
    if (change === 'view') await wrapper.setProps({ view: 'myRequests' })
    else { service.user = { ...member, id: 3, username: 'bob' }; service.keys = []; await wrapper.setProps({ user: service.user }) }
    await flushPromises()
    expect(wrapper.find('[data-testid="one-time-secret"]').exists()).toBe(false)
    await wrapper.setProps({ view: 'myKeys', user: member }); await flushPromises()
    expect(wrapper.html()).not.toContain(oneTimeSecret)
  })

  it.each(['view', 'user', 'unmount'])('签发响应未返回时 %s：迟到响应不得泄漏 secret 或重开弹窗', async (change) => {
    const pending = deferred(); server((call) => call.method === 'POST' && call.url === '/api/self/api-keys' ? pending.promise : undefined)
    const wrapper = await pane(); await openCreate(wrapper)
    await wrapper.get('[data-testid="key-form"]').trigger('submit')
    if (change === 'unmount') wrapper.unmount()
    else if (change === 'view') await wrapper.setProps({ view: 'account' })
    else { service.user = { ...member, id: 3, username: 'bob' }; service.keys = []; await wrapper.setProps({ user: service.user }) }
    pending.resolve(response({ key: { id: 99 }, secret: oneTimeSecret }, 201)); await flushPromises()
    expect(document.body.textContent).not.toContain(oneTimeSecret)
    if (change === 'unmount') return
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    await wrapper.setProps({ view: 'myKeys' }); await flushPromises()
    expect(wrapper.find('[data-testid="one-time-secret"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="new-key"]').attributes('disabled')).toBeUndefined()
  })

  it('密码在视图/账号切换时清空；迟到密码成功不发 user-refresh 给下一账号', async () => {
    const pending = deferred(); server((call) => call.url === '/api/self/password' ? pending.promise : undefined)
    const wrapper = await pane('account')
    await wrapper.get('[data-testid="current-password"]').setValue('CurrentPassword!42'); await fillPassword(wrapper)
    await wrapper.get('[data-testid="password-form"]').trigger('submit')
    await wrapper.setProps({ view: 'myKeys' }); await flushPromises()
    service.user = { ...member, id: 3, username: 'bob' }
    await wrapper.setProps({ view: 'account', user: service.user }); await flushPromises()
    expect(wrapper.findAll('input[type="password"]').every((node) => (node.element as HTMLInputElement).value === '')).toBe(true)
    pending.resolve(response(null, 204)); await flushPromises()
    expect(wrapper.emitted('user-refresh')).toBeUndefined()
    expect(wrapper.text()).not.toContain('密码已更新')
    expect(wrapper.get('[data-testid="new-password"]').attributes('disabled')).toBeUndefined()
  })

  it('迟到列表/能力查询不覆盖下一账号和新的密钥选择', async () => {
    const oldKeys = deferred(); let first = true
    server((call) => {
      if (call.url === '/api/self/api-keys' && first) { first = false; return oldKeys.promise }
      return undefined
    })
    const wrapper = await pane()
    service.user = { ...member, id: 3, username: 'bob' }; service.keys = [{ ...initialKey, id: 6, name: 'bob-key' }]
    await wrapper.setProps({ user: service.user }); await flushPromises()
    oldKeys.resolve(response([{ ...initialKey, name: 'old-account-key' }])); await flushPromises()
    expect(wrapper.text()).toContain('bob-key'); expect(wrapper.text()).not.toContain('old-account-key')
    service.keys.push({ ...initialKey, id: 7, name: 'new-selection' })
    const oldAccess = deferred()
    server((call) => call.url === '/api/self/access?api_key_id=6' ? oldAccess.promise : undefined)
    await button(wrapper, '刷新密钥').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="access-key"]').setValue('7'); await flushPromises()
    oldAccess.resolve(response({ api_key_id: 6, key_status: 'active', models: [{ ...chat, id: 'late-alias' }] })); await flushPromises()
    expect(wrapper.get('[data-testid="access-alias"]').text()).not.toContain('late-alias')
  })

  it('旧分页响应不覆盖恢复后的 URL 上下文，剪贴板迟到拒绝不在下一视图创建明文回退', async () => {
    const oldPage = deferred(); let first = true
    server((call) => { if (call.url.startsWith('/api/self/requests') && first) { first = false; return oldPage.promise } return undefined })
    const wrapper = await pane('myRequests')
    service.records = [{ ...initialRecord, id: 'new-page-id' }]
    await wrapper.setProps({ context: { page: '2' } }); await flushPromises()
    oldPage.resolve(response([{ ...initialRecord, id: 'late-page-id' }])); await flushPromises()
    expect(wrapper.text()).toContain('new-page-id'); expect(wrapper.text()).not.toContain('late-page-id')
    await wrapper.setProps({ view: 'myKeys' }); await flushPromises(); await create(wrapper)
    const clipboardPending = deferred(); clipboard({ writeText: vi.fn(() => clipboardPending.promise) })
    const exec = vi.fn(() => true); (document as any).execCommand = exec
    await button(wrapper, '复制密钥').trigger('click')
    await wrapper.setProps({ view: 'myRequests' }); await flushPromises()
    clipboardPending.reject(new Error('denied after view switch')); await flushPromises()
    expect(exec).not.toHaveBeenCalled()
    expect(wrapper.html()).not.toContain(oneTimeSecret)
    expect(document.querySelectorAll('body > textarea')).toHaveLength(0)
  })
})

describe('个人控制台边界回归', () => {
  it('首把密钥无需管理端目录，明确空白名单；外来归属行不会成为接入选项', async () => {
    service.keys = [{ ...initialKey, user_id: 999, name: 'other-owner-key' }]
    const wrapper = await pane()
    expect(wrapper.text()).not.toContain('other-owner-key')
    expect(wrapper.text()).toContain('尚无本人密钥')
    expect(calls.some(call => call.url.startsWith('/api/self/access'))).toBe(false)
    await openCreate(wrapper)
    expect(wrapper.get('[role="dialog"]').text()).toContain('可先以空白名单创建')
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    expect(writes()[0].body.model_allow).toEqual([])
    expect((wrapper.get('[data-testid="access-key"]').element as HTMLSelectElement).value).toBe('9')
    expect(calls.every(call => call.url.startsWith('/api/self/'))).toBe(true)
  })

  it('编辑名称不改写未变更到期时间的秒数，也不要求自动给已过期密钥续期', async () => {
    service.keys[0].expires_at = '2000-01-01T00:00:59Z'
    const wrapper = await pane()
    await button(wrapper.get('[data-key-id="5"]'), '编辑').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="key-name"]').setValue('renamed-expired-key')
    await wrapper.get('[data-testid="key-form"]').trigger('submit'); await flushPromises()
    expect(writes()[0].body).toEqual({ name: 'renamed-expired-key', model_allow: [] })
    expect(service.keys[0].expires_at).toBe('2000-01-01T00:00:59Z')
    expect(wrapper.get('[data-key-id="5"]').text()).toContain('已过期')
  })

  it('编辑的重复提交被锁定；切换后锁定到结果返回，但旧结果不发新视图的成功提示', async () => {
    const pending = deferred(); server(call => call.method === 'PATCH' ? pending.promise : undefined)
    const wrapper = await pane()
    await button(wrapper.get('[data-key-id="5"]'), '编辑').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="key-name"]').setValue('edited-name')
    await wrapper.get('[data-testid="key-form"]').trigger('submit')
    await wrapper.get('[data-testid="key-form"]').trigger('submit')
    expect(writes()).toHaveLength(1)
    await wrapper.setProps({ view: 'account' }); await flushPromises()
    await wrapper.setProps({ view: 'myKeys' }); await flushPromises()
    expect(wrapper.get('[data-testid="new-key"]').attributes('disabled')).toBeDefined()
    pending.resolve(response({ ...initialKey, name: 'edited-name' })); await flushPromises()
    expect(wrapper.text()).not.toContain('密钥已更新')
    expect(wrapper.get('[data-testid="new-key"]').attributes('disabled')).toBeUndefined()
  })

  it.each(['close', 'view', 'user', 'unmount'])('%s 清理包括已经移除的明文 DOM 引用', async change => {
    const wrapper = await pane(); await create(wrapper)
    const element = wrapper.get('[data-testid="one-time-secret"]').element as HTMLTextAreaElement
    expect(element.value).toBe(oneTimeSecret)
    if (change === 'close') await button(wrapper, '我已安全保存').trigger('click')
    else if (change === 'view') await wrapper.setProps({ view: 'account' })
    else if (change === 'user') { service.user = { ...member, id: 3, username: 'bob' }; await wrapper.setProps({ user: service.user }) }
    else wrapper.unmount()
    await flushPromises()
    expect(element.value).toBe('')
    expect(element.textContent).toBe('')
  })

  it('清空和卸载同时擦除密码 DOM 值；未知或外来 summary 不放开密码能力', async () => {
    const wrapper = await pane('account')
    await fillPassword(wrapper)
    const input = wrapper.get('[data-testid="new-password"]').element as HTMLInputElement
    await button(wrapper, '清空密码输入').trigger('click')
    expect(input.value).toBe('')
    await fillPassword(wrapper); wrapper.unmount()
    expect(input.value).toBe('')
    service.user = { ...sso, id: 999, username: 'foreign' }
    const next = await pane('account', { id: 2, username: 'alice', role: 'user' })
    expect(next.text()).toContain('账号数据无效')
    expect(next.get('[data-testid="new-password"]').attributes('disabled')).toBeDefined()
  })

  it('实际请求错误 code 不来自 type；滤镜无效 ID 不查询，缺失 code 不伪造', async () => {
    service.records = [{ ...initialRecord, status: 'error', error_code: 'actual_runtime_code' }, { ...initialRecord, id: 'type-only-record', status: 'error', error_type: 'timeout' }]
    const wrapper = await pane('myRequests')
    expect(wrapper.findAll('.request-row')[0].text()).toContain('code: actual_runtime_code')
    expect(wrapper.findAll('.request-row')[1].text()).not.toContain('code:')
    await wrapper.get('[data-testid="filter-request-id"]').setValue('not an actual ID')
    const reads = calls.length
    await wrapper.get('[data-testid="request-filters"]').trigger('submit')
    expect(wrapper.text()).toContain('请求 ID 格式无效')
    expect(calls).toHaveLength(reads)
    expect(wrapper.emitted('context-change')).toBeUndefined()
  })

  it('恢复 URL 的秒/毫秒时间精度，修改其他筛选不悄悄截断时间范围', async () => {
    const from = '2026-10-01T01:02:03.123Z', to = '2026-10-03T04:05:06.456Z'
    const wrapper = await pane('myRequests', member, { page: '2', from, to })
    expect((wrapper.get('[data-testid="filter-from"]').element as HTMLInputElement).value).toBe('2026-10-01T01:02:03.123')
    await wrapper.get('[data-testid="filter-status"]').setValue('error')
    await wrapper.get('[data-testid="request-filters"]').trigger('submit'); await flushPromises()
    expect(lastRequest().searchParams.get('from')).toBe(from)
    expect(lastRequest().searchParams.get('to')).toBe(to)
    expect(last(wrapper.emitted('context-change'))).toEqual([{ page: '1', status: 'error', from, to }])
  })
})
