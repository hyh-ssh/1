import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import App from './App.vue'
import { setCSRFToken } from './api'

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => (typeof body === 'string' ? body : JSON.stringify(body)) }
}
type Call = { url: string; method: string; body: any; headers: Record<string, string> }
let calls: Call[] = []
let session: any = null
let keys: any[] = []
let records: any[] = []
const wrappers: VueWrapper[] = []

const member = { id: 2, username: 'alice', display_name: '爱丽丝', role: 'user', password_login_enabled: true, password_capabilities: { local_enabled: true, can_set_without_current: false, can_recover: false } }
const admin = { id: 1, username: 'admin', display_name: '管理员昵称', role: 'admin', password_login_enabled: true, password_capabilities: { local_enabled: true, can_set_without_current: false, can_recover: false } }
const usage = { requests: 12, success: 11, errors: 1, success_rate: 91.7, avg_latency_ms: 320, total_tokens: 4567, input_tokens: 3000, output_tokens: 1567, cost: '1.230000', credits: '12.345' }
const ownKey = { id: 5, user_id: 2, name: 'alice-key', key_prefix: 'sk-llm-a', key_suffix: '1234', active: true, model_allow: ['gpt-x'], expires_at: null, last_used_at: null, key_hash: 'must-not-render' }
const ownRequest = { id: 'stored-request-7', model_alias: 'gpt-x', status: 'success', status_code: 200, usage: { meters: { total: 1234 }, estimated: false }, credits: '0.500', cost: '0.010000', total_latency_ms: 250, request_body: 'private prompt must not render' }

function recordCall(url: string, init: RequestInit) {
  const headers = (init.headers || {}) as Record<string, string>
  calls.push({ url, method: (init.method || 'GET').toUpperCase(), body: init.body ? JSON.parse(String(init.body)) : undefined, headers })
}
function stubServer(overrides: (url: string, init: RequestInit) => unknown = () => undefined) {
  vi.stubGlobal('fetch', vi.fn((url: string, init: RequestInit = {}) => {
    recordCall(url, init)
    const override = overrides(url, init)
    if (override !== undefined) return Promise.resolve(override)
    if (url === '/api/auth/me') return Promise.resolve(session ? response({ user: session, csrf_token: 'session-csrf' }) : response({ error: { message: 'authentication required' } }, 401))
    if (url === '/api/auth/login') {
      const posted = JSON.parse(String(init.body))
      session = { ...(posted.username === 'admin' ? admin : member), username: posted.username }
      return Promise.resolve(response({ user: session, csrf_token: 'session-csrf' }))
    }
    if (url === '/api/auth/logout') { session = null; return Promise.resolve(response(null, 204)) }
    if (url === '/api/auth/oidc/config') return Promise.resolve(response({ enabled: false }))
    if (url === '/api/self/summary') return Promise.resolve(response({ user: session, usage, limits: [{ id: 9, scope: 'user', scope_id: session?.id, model_id: '', rpm: 60, tpm: 0, concurrency: 0, active: true }], budgets: [{ cost_limit: '1000000', marker: 'retired-budget-data' }] }))
    if (url === '/api/self/api-keys' && (init.method || 'GET').toUpperCase() === 'GET') return Promise.resolve(response(keys))
    if (url === '/api/self/api-keys' && (init.method || 'GET').toUpperCase() === 'POST') {
      const created = { ...ownKey, id: 11, name: JSON.parse(String(init.body)).name, user_id: session?.id }
      keys = [...keys, created]
      return Promise.resolve(response({ key: created, secret: 'one-time-secret-fixture' }, 201))
    }
    if (/^\/api\/self\/api-keys\/\d+$/.test(url)) {
      const id = Number(url.split('/').pop())
      if ((init.method || 'GET').toUpperCase() === 'PATCH') {
        keys = keys.map(key => key.id === id ? { ...key, ...JSON.parse(String(init.body)) } : key)
        return Promise.resolve(response(keys.find(key => key.id === id)))
      }
      if ((init.method || 'GET').toUpperCase() === 'DELETE') { keys = keys.filter(key => key.id !== id); return Promise.resolve(response(null, 204)) }
    }
    if (url.startsWith('/api/self/access')) {
      const id = Number(new URL(url, 'http://localhost').searchParams.get('api_key_id'))
      return Promise.resolve(response({ api_key_id: id, key_status: 'active', first_success_at: null, models: [{ id: 'gpt-x', protocols: ['chat_completions'], capabilities: null, pricing_status: 'priced', ready: true, reasons: [] }] }))
    }
    if (url.startsWith('/api/self/requests')) return Promise.resolve(response(records))
    if (url === '/api/self/password') return Promise.resolve(response({ status: 'ok' }))
    if (url === '/api/admin/stats') return Promise.resolve(response({}))
    if (url.startsWith('/api/admin/runtime')) return Promise.resolve(response({}))
    if (url.startsWith('/api/admin/')) return Promise.resolve(response([]))
    return Promise.resolve(response({}, 404))
  }))
}
async function mountApp() {
  const wrapper = mount(App)
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
async function signIn(username = 'alice') {
  const wrapper = await mountApp()
  const inputs = wrapper.find('.login-card').findAll('input')
  await inputs[0]!.setValue(username)
  await inputs[1]!.setValue('Secret!2345')
  await wrapper.find('.login-card form').trigger('submit')
  await flushPromises()
  return wrapper
}
function nav(wrapper: VueWrapper, label: string) {
  return wrapper.findAll('aside nav button').find(button => button.text().includes(label))!
}
function button(wrapper: VueWrapper, label: string) {
  return wrapper.findAll('button').find(item => item.text().trim() === label)!
}
function personalAlert(wrapper: VueWrapper) { return wrapper.find('.personal-pane [role="alert"]') }
async function openKeys(wrapper: VueWrapper) { await nav(wrapper, '我的 API 密钥').trigger('click'); await flushPromises() }
async function submitKeyForm(wrapper: VueWrapper) { await wrapper.find('[data-testid="key-form"]').trigger('submit'); await flushPromises() }
function stubClipboard(value: unknown) { Object.defineProperty(navigator, 'clipboard', { value, configurable: true, writable: true }) }
function localInput(date: Date) {
  const pad = (number: number) => String(number).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

beforeEach(() => {
  sessionStorage.clear(); localStorage.clear(); setCSRFToken('')
  window.history.replaceState({}, '', '#')
  vi.stubGlobal('scrollTo', vi.fn())
  calls = []; session = null; keys = [{ ...ownKey }]; records = [{ ...ownRequest }]
  stubClipboard(undefined)
  stubServer()
})
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  setCSRFToken(''); vi.unstubAllGlobals(); document.body.innerHTML = ''
})

describe('个人控制台在整壳中的角色与数据边界', () => {
  it('普通账号默认进入账号中心：用量和限流可见、预算不显示，且不访问管理 API', async () => {
    const wrapper = await signIn()
    expect(wrapper.findAll('aside nav button').map(item => item.text())).toEqual(['账号中心', '我的 API 密钥', '我的调用记录'])
    expect(wrapper.find('aside nav button.active').text()).toBe('账号中心')
    expect(wrapper.get('#personal-account-title').text()).toBe('账号中心 · alice')
    expect(wrapper.text()).toContain('12 / 11 / 1')
    expect(wrapper.text()).toContain('4,567 Token')
    expect(wrapper.text()).toContain('¥1.230000')
    expect(wrapper.text()).toContain('12.345 积分')
    expect(wrapper.text()).toContain('RPM 60')
    expect(wrapper.text()).not.toContain('预算')
    expect(wrapper.text()).not.toContain('retired-budget-data')
    expect(calls.every(call => call.url.startsWith('/api/auth/') || call.url.startsWith('/api/self/'))).toBe(true)
    expect(calls.some(call => call.url.startsWith('/api/admin/'))).toBe(false)
  })

  it('管理员也通过同一账号中心组件读取本人数据和本人密钥接口', async () => {
    session = admin
    window.history.replaceState({}, '', '#/account')
    stubServer()
    const wrapper = await mountApp()
    expect(wrapper.find('.personal-pane #personal-account-title').text()).toBe('账号中心 · admin')
    expect(wrapper.find('.username-entry').text()).toBe('admin')
    expect(calls.some(call => call.url === '/api/self/summary')).toBe(true)
    await nav(wrapper, '我的 API 密钥').trigger('click')
    await flushPromises()
    expect(wrapper.find('.personal-pane #personal-keys-title').exists()).toBe(true)
    expect(calls.some(call => call.url === '/api/self/api-keys')).toBe(true)
    expect(calls.some(call => call.url === '/api/admin/api-keys')).toBe(false)
  })

  it('本人密钥只显示掩码/最后使用信息，不渲染哈希；本人请求使用 session scope', async () => {
    const wrapper = await signIn()
    await openKeys(wrapper)
    expect(wrapper.text()).toContain('alice-key')
    expect(wrapper.text()).toContain('sk-llm-a…1234')
    expect(wrapper.text()).toContain('白名单：gpt-x')
    expect(wrapper.text()).toContain('从未使用')
    expect(wrapper.html()).not.toContain('must-not-render')

    await nav(wrapper, '我的调用记录').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('stored-request-7')
    expect(wrapper.text()).toContain('1,234 Token')
    expect(wrapper.text()).not.toContain('private prompt')
    const selfRequests = calls.filter(call => call.url.startsWith('/api/self/requests'))
    expect(selfRequests.length).toBeGreaterThan(0)
    expect(selfRequests.every(call => !call.url.includes('user_id'))).toBe(true)
    expect(calls.some(call => call.url.startsWith('/api/admin/'))).toBe(false)
  })

  it('成功请求和免费零金额用量仍显示正请求数与 Token，不把零金额当作无请求', async () => {
    const freeUsage = { ...usage, requests: 8, success: 8, errors: 0, success_rate: 100, total_tokens: 2345, cost: '0.000000', credits: '0.000' }
    records = [{ ...ownRequest, id: 'free-request', model_alias: 'free-model', usage: { meters: { total: 2345 }, estimated: true }, cost: '0.000000', credits: '0.000', settlement_status: 'free' }]
    stubServer(url => url === '/api/self/summary' ? response({ user: session, usage: freeUsage, limits: [] }) : undefined)
    const wrapper = await signIn()
    expect(wrapper.text()).toContain('8 / 8 / 0')
    expect(wrapper.text()).toContain('2,345 Token')
    expect(wrapper.text()).toContain('¥0.000000')
    expect(wrapper.text()).toContain('0.000 积分')
    await nav(wrapper, '我的调用记录').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('free-model')
    expect(wrapper.text()).toContain('2,345 Token')
    expect(wrapper.text()).toContain('¥0.000000 CNY')
    expect(wrapper.text()).toContain('免费（显式零费率）')
    expect(calls.some(call => call.url.startsWith('/api/admin/'))).toBe(false)
  })
})

describe('权限错误、退出与密钥治理', () => {
  it('本人摘要遇到 403 时显示真实的管理员限制提示，而非密码或网络错误', async () => {
    stubServer(url => url === '/api/self/summary' ? response({ error: { message: 'administrator privileges required', code: 'administrator_required' } }, 403) : undefined)
    const wrapper = await signIn()
    expect(personalAlert(wrapper).text()).toContain('HTTP 403')
    expect(personalAlert(wrapper).text()).toContain('code: administrator_required')
    expect(wrapper.text()).not.toContain('用户名或密码错误')
    expect(calls.some(call => call.url.startsWith('/api/admin/'))).toBe(false)
  })

  it('停用账号的登录响应明确显示账号已停用，而非密码错误', async () => {
    stubServer(url => url === '/api/auth/login' ? response({ error: { message: 'account is disabled', code: 'account_disabled' } }, 403) : undefined)
    const wrapper = await mountApp()
    const inputs = wrapper.find('.login-card').findAll('input')
    await inputs[0]!.setValue('alice'); await inputs[1]!.setValue('Secret!2345')
    await wrapper.find('.login-card form').trigger('submit'); await flushPromises()
    expect(wrapper.find('.login-card [role="alert"]').text()).toContain('账号已停用')
    expect(wrapper.find('.login-card [role="alert"]').text()).not.toContain('用户名或密码错误')
  })

  it('退出再登录另一账号时不保留前一个账号的密钥或迟到错误', async () => {
    let bobKeysFail = false
    stubServer((url, init) => {
      if (url === '/api/self/api-keys' && session?.username === 'bob' && bobKeysFail) return response({ error: { message: 'administrator store operation failed', code: 'admin_store_error' } }, 500)
      if (url === '/api/auth/login' && JSON.parse(String(init.body)).username === 'bob') {
        session = { ...member, id: 3, username: 'bob' }
        return response({ user: session, csrf_token: 'session-csrf' })
      }
      return undefined
    })
    const wrapper = await signIn('alice')
    await openKeys(wrapper)
    expect(wrapper.text()).toContain('alice-key')
    await wrapper.find('.logout').trigger('click')
    await flushPromises()
    expect(wrapper.find('.login-card').exists()).toBe(true)
    bobKeysFail = true
    const inputs = wrapper.find('.login-card').findAll('input')
    await inputs[0]!.setValue('bob'); await inputs[1]!.setValue('Secret!2345')
    await wrapper.find('.login-card form').trigger('submit'); await flushPromises()
    await openKeys(wrapper)
    expect(wrapper.text()).not.toContain('alice-key')
    expect(wrapper.find('.personal-pane [role="alert"]').text()).toContain('HTTP 500')
    expect(wrapper.find('.personal-pane [role="alert"]').text()).toContain('admin_store_error')
  })

  it('管理员密钥治理只提供查看和停用他人密钥，不提供新建、修改范围、重启或删除', async () => {
    session = admin
    window.history.replaceState({}, '', '#/keys')
    const rows = [
      { id: 5, user_id: 2, username: 'alice', name: 'alice-key', key_prefix: 'sk-a', key_suffix: '1234', active: true, model_allow: ['gpt-x'] },
      { id: 6, user_id: 1, username: 'admin', name: 'admin-key', key_prefix: 'sk-b', key_suffix: '5678', active: true, model_allow: [] },
    ]
    const confirm = vi.fn(() => true)
    vi.stubGlobal('confirm', confirm)
    stubServer((url, init) => {
      if (url === '/api/admin/api-keys' && (init.method || 'GET').toUpperCase() === 'GET') return response(rows)
      if (url === '/api/admin/users') return response([{ ...admin }, { ...member }])
      if (url === '/api/admin/api-keys/5' && String(init.method).toUpperCase() === 'PATCH') return response({ ...rows[0], active: false })
      return undefined
    })
    const wrapper = await mountApp()
    const userRow = wrapper.findAll('.table-row').find(row => row.text().includes('alice-key'))!
    expect(userRow.findAll('button').map(item => item.text())).toEqual(['停用'])
    const ownRow = wrapper.findAll('.table-row').find(row => row.text().includes('admin-key'))!
    expect(ownRow.findAll('button').map(item => item.text())).toEqual(['本人管理'])
    expect(userRow.text()).toContain('停用')
    expect(userRow.findAll('button').some(item => ['删除', '编辑', '启用', '本人管理'].includes(item.text()))).toBe(false)
    expect(wrapper.findAll('aside nav button').map(item => item.text())).toContain('密钥治理')
    expect(wrapper.findAll('button').some(item => item.text().includes('新建'))).toBe(false)
    expect(userRow.findAll('button').some(item => ['删除', '编辑', '启用'].includes(item.text()))).toBe(false)
    await userRow.find('button').trigger('click'); await flushPromises()
    expect(confirm).toHaveBeenCalled()
    const update = calls.find(call => call.method === 'PATCH' && call.url === '/api/admin/api-keys/5')!
    expect(update.body).toEqual({ active: false })
    expect(calls.some(call => call.method === 'POST' && call.url.startsWith('/api/admin/api-keys'))).toBe(false)
    expect(calls.some(call => call.method === 'DELETE' && call.url.startsWith('/api/admin/api-keys'))).toBe(false)
  })
})

describe('本人密钥弹窗、生命周期与密码', () => {
  it('为本人签发时不传 user_id；一次性明文仅在弹窗显示并可复制后显式清除', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard({ writeText })
    const wrapper = await signIn()
    await openKeys(wrapper)
    await wrapper.get('[data-testid="new-key"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="key-name"]').setValue('ci-key')
    await submitKeyForm(wrapper)

    const created = calls.find(call => call.method === 'POST' && call.url === '/api/self/api-keys')!
    expect(created.body).toEqual({ name: 'ci-key', model_allow: [] })
    expect(created.headers['X-CSRF-Token']).toBe('session-csrf')
    expect(created.body).not.toHaveProperty('user_id')
    expect(wrapper.get('[data-testid="one-time-secret"]').element).toHaveProperty('value', 'one-time-secret-fixture')
    expect(wrapper.find('.notice').exists()).toBe(false)
    await button(wrapper, '复制密钥').trigger('click'); await flushPromises()
    expect(writeText).toHaveBeenCalledWith('one-time-secret-fixture')
    expect(wrapper.get('[role="dialog"]').text()).toContain('已复制')
    await button(wrapper, '我已安全保存').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.html()).not.toContain('one-time-secret-fixture')
    await nav(wrapper, '我的调用记录').trigger('click'); await flushPromises()
    expect(wrapper.html()).not.toContain('one-time-secret-fixture')
  })

  it('创建密钥拒绝过去时间；有效到期按 UTC 发送，编辑可修改到期与白名单', async () => {
    const wrapper = await signIn()
    await openKeys(wrapper)
    await wrapper.get('[data-testid="new-key"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="key-name"]').setValue('expiring-key')
    const past = new Date(Date.now() - 86_400_000)
    await wrapper.get('[data-testid="key-expiry"]').setValue(localInput(past))
    await submitKeyForm(wrapper)
    expect(wrapper.get('[role="dialog"] [role="alert"]').text()).toContain('过期时间必须晚于当前时间')
    expect(calls.filter(call => call.method === 'POST' && call.url === '/api/self/api-keys')).toHaveLength(0)

    const future = new Date(Date.now() + 86_400_000)
    const futureInput = localInput(future)
    await wrapper.get('[data-testid="key-expiry"]').setValue(futureInput)
    await submitKeyForm(wrapper)
    expect(calls.find(call => call.method === 'POST' && call.url === '/api/self/api-keys')!.body.expires_at).toBe(new Date(futureInput).toISOString())
    await button(wrapper, '我已安全保存').trigger('click'); await flushPromises()

    const row = wrapper.get('[data-key-id="5"]')
    await row.findAll('button').find(item => item.text() === '编辑')!.trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="key-name"]').setValue('renamed-key')
    const editedExpiry = localInput(new Date(Date.now() + 172_800_000))
    await wrapper.get('[data-testid="key-expiry"]').setValue(editedExpiry)
    const clearAllow = wrapper.findAll('button').find(item => item.text().includes('清空白名单'))
    if (clearAllow) await clearAllow.trigger('click')
    await submitKeyForm(wrapper)
    const edited = calls.find(call => call.method === 'PATCH' && call.url === '/api/self/api-keys/5')!
    expect(edited.body).toMatchObject({ name: 'renamed-key', expires_at: new Date(editedExpiry).toISOString(), model_allow: [] })
    expect(edited.headers['X-CSRF-Token']).toBe('session-csrf')
  })

  it('停用和删除本人密钥都必须在本人确认弹窗操作', async () => {
    const wrapper = await signIn()
    await openKeys(wrapper)
    await wrapper.get('[data-key-id="5"]').findAll('button').find(item => item.text() === '停用')!.trigger('click')
    expect(calls.some(call => call.method === 'PATCH')).toBe(false)
    await wrapper.get('[data-testid="confirm-key-action"]').trigger('click'); await flushPromises()
    expect(calls.find(call => call.method === 'PATCH' && call.url === '/api/self/api-keys/5')!.body).toEqual({ active: false })
    expect(wrapper.text()).toContain('密钥已停用')
    await wrapper.get('[data-key-id="5"]').findAll('button').find(item => item.text() === '删除')!.trigger('click')
    expect(calls.some(call => call.method === 'DELETE')).toBe(false)
    expect(wrapper.get('[role="dialog"]').text()).toContain('删除不可恢复')
    await wrapper.get('[data-testid="confirm-key-action"]').trigger('click'); await flushPromises()
    const deleted = calls.find(call => call.method === 'DELETE' && call.url === '/api/self/api-keys/5')!
    expect(deleted.headers['X-CSRF-Token']).toBe('session-csrf')
    expect(wrapper.text()).toContain('密钥已删除')
    expect(calls.every(call => call.url.startsWith('/api/auth/') || call.url.startsWith('/api/self/'))).toBe(true)
  })

  it('本地密码需要原密码且核验确认值；成功带 CSRF 并清空输入', async () => {
    const wrapper = await signIn()
    await wrapper.get('[data-testid="current-password"]').setValue('Current!2345')
    await wrapper.get('[data-testid="new-password"]').setValue('Next!2345')
    await wrapper.get('[data-testid="confirm-password"]').setValue('Mismatch!2345')
    await wrapper.get('[data-testid="password-form"]').trigger('submit')
    expect(calls.some(call => call.url === '/api/self/password')).toBe(false)

    await wrapper.get('[data-testid="confirm-password"]').setValue('Next!2345')
    await wrapper.get('[data-testid="password-form"]').trigger('submit'); await flushPromises()
    const posted = calls.find(call => call.url === '/api/self/password')!
    expect(posted.body).toEqual({ current_password: 'Current!2345', new_password: 'Next!2345' })
    expect(posted.headers['X-CSRF-Token']).toBe('session-csrf')
    expect(wrapper.find('.personal-success').text()).toContain('其他会话已失效')
    expect(wrapper.findAll('input[type="password"]').map(input => (input.element as HTMLInputElement).value)).toEqual(['', '', ''])
  })
})
