import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import App from './App.vue'
import { setCSRFToken } from './api'

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => (typeof body === 'string' ? body : JSON.stringify(body)) }
}
type Call = { url: string; method: string; body: any }
let calls: Call[] = []
let session: any = null
const wrappers: VueWrapper[] = []
const usage = { requests: 3, success: 3, errors: 0, success_rate: 100, avg_latency_ms: 120, total_tokens: 300, cost: '0.000000', credits: '0.000' }
const member = { id: 2, username: 'alice', display_name: '爱丽丝', role: 'user', password_login_enabled: true, password_capabilities: { local_enabled: true, can_set_without_current: false, can_recover: false } }
const admin = { id: 1, username: 'admin', display_name: '总管', role: 'admin', password_login_enabled: true, password_capabilities: { local_enabled: true, can_set_without_current: false, can_recover: false } }
function stubServer(overrides: (url: string, init: RequestInit) => unknown = () => undefined) {
  vi.stubGlobal('fetch', vi.fn((url: string, init: RequestInit = {}) => {
    const method = (init.method || 'GET').toUpperCase()
    calls.push({ url, method, body: init.body ? JSON.parse(String(init.body)) : undefined })
    const custom = overrides(url, init)
    if (custom !== undefined) return Promise.resolve(custom)
    if (url === '/api/auth/me') return Promise.resolve(session ? response({ user: session, csrf_token: 'csrf-token' }) : response({ error: { message: 'authentication required' } }, 401))
    if (url === '/api/auth/login') {
      const posted = JSON.parse(String(init.body)).username
      session = { ...member, username: posted }
      return Promise.resolve(response({ user: session, csrf_token: 'csrf-token' }))
    }
    if (url === '/api/auth/logout') { session = null; return Promise.resolve(response(null, 204)) }
    if (url === '/api/auth/oidc/config') return Promise.resolve(response({ enabled: false }))
    if (url.startsWith('/api/self/summary')) return Promise.resolve(response({ user: session, usage, limits: [], budgets: [] }))
    if (url.startsWith('/api/self/api-keys')) return Promise.resolve(response([]))
    if (url.startsWith('/api/self/requests')) return Promise.resolve(response([]))
    if (url === '/api/admin/oidc-provider') return Promise.resolve(response({ enabled: false, issuer: '', client_id: '', redirect_url: '', display_name: '', has_client_secret: false, ready: false }))
    if (url.startsWith('/api/admin/stats') || url.startsWith('/api/admin/runtime')) return Promise.resolve(response({ requests: 0, success_rate: 0, cost: 0, credits: 0, total_tokens: 0, trend: [], routes: [] }))
    if (url.startsWith('/api/admin/')) return Promise.resolve(response([]))
    if (url.startsWith('/readyz')) return Promise.resolve(response('ok'))
    return Promise.resolve(response({}, 404))
  }))
}
async function mountApp(options: Parameters<typeof mount>[1] = {}) {
  const wrapper = mount(App, options)
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
async function submitLogin(wrapper: VueWrapper, username: string, password = 'Secret!2345') {
  const inputs = wrapper.find('.login-card').findAll('input')
  await inputs[0]!.setValue(username); await inputs[1]!.setValue(password)
  await wrapper.find('.login-card form').trigger('submit'); await flushPromises()
}
function lastCall(prefix: string) { return [...calls].reverse().find(call => call.url.startsWith(prefix)) }
function params(url: string) { return new URL(url, 'http://gateway.test').searchParams }
function modelCalls() { return calls.filter(call => call.url.startsWith('/v1/') || call.url.startsWith('/api/v1/')) }

beforeEach(() => {
  sessionStorage.clear(); localStorage.clear(); setCSRFToken(''); calls = []; session = null
  window.history.replaceState({}, '', '#')
  vi.stubGlobal('scrollTo', vi.fn())
})
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  setCSRFToken(''); vi.unstubAllGlobals(); window.history.replaceState({}, '', '#'); document.body.innerHTML = ''
})

describe('hash 路由、权限回退和前进/后退', () => {
  it.each(['#/users', '#/settings?secret=NEVER-STORE', '#/userGroups', '#/budgets'])('普通用户直达 %s 时回退到账号中心且不请求管理数据', async hash => {
    session = member
    window.history.replaceState({}, '', hash)
    stubServer()
    const wrapper = await mountApp()
    expect(wrapper.find('[role="alert"]').text()).toContain('此链接无效或当前账号无权访问')
    expect(window.location.hash).toBe('#/account')
    expect(window.location.href).not.toContain('NEVER-STORE')
    expect(wrapper.text()).toContain('账号中心')
    expect(calls.some(call => call.url.startsWith('/api/admin/'))).toBe(false)
  })

  it('管理员允许的页面保留稳定 hash，但未知/敏感查询字段不会进入 URL 或页面', async () => {
    session = admin
    window.history.replaceState({}, '', '#/settings?secret=NEVER-STORE&token=PRIVATE')
    stubServer()
    const wrapper = await mountApp()
    expect(window.location.hash).toBe('#/settings')
    expect(wrapper.text()).toContain('设置')
    expect(wrapper.text()).not.toContain('NEVER-STORE')
    expect(window.location.hash).not.toContain('PRIVATE')
  })

  it('管理员的本人密钥深链走 self API，不误入治理密钥端点', async () => {
    session = admin
    window.history.replaceState({}, '', '#/myKeys?key=5')
    stubServer()
    const wrapper = await mountApp()
    expect(window.location.hash).toBe('#/myKeys?key=5')
    expect(wrapper.find('#personal-keys-title').exists()).toBe(true)
    expect(calls.some(call => call.url.startsWith('/api/self/api-keys'))).toBe(true)
    expect(calls.some(call => call.url.startsWith('/api/admin/api-keys'))).toBe(false)
  })

  it('浏览器前进与后退上下文恢复列表筛选和分页 offset', async () => {
    session = admin
    window.history.replaceState({}, '', '#/requests?page=2&status=error')
    stubServer()
    await mountApp()
    expect(params(lastCall('/api/admin/requests')!.url).get('offset')).toBe('50')
    expect(params(lastCall('/api/admin/requests')!.url).get('status')).toBe('error')

    window.history.pushState({ scrollY: 0 }, '', '#/requests?status=error')
    window.dispatchEvent(new PopStateEvent('popstate')); await flushPromises()
    expect(params(lastCall('/api/admin/requests')!.url).get('offset')).toBe('0')
    expect(window.scrollTo).toHaveBeenCalled()

    window.history.pushState({ scrollY: 18 }, '', '#/requests?page=2&status=error')
    window.dispatchEvent(new PopStateEvent('popstate')); await flushPromises()
    expect(params(lastCall('/api/admin/requests')!.url).get('offset')).toBe('50')
  })

  it('详情上下文由 child event 写入 URL 白名单，正文/密钥从不写进 hash', async () => {
    session = admin
    window.history.replaceState({}, '', '#/requests?page=1&status=success')
    stubServer()
    const RequestContextEmitter = defineComponent({
      props: { view: { type: String, required: true }, context: { type: Object, default: () => ({}) } },
      emits: ['context-change'],
      setup(props, { emit }) {
        return () => h('button', { onClick: () => emit('context-change', { ...props.context, detail: 'stored-request-id', body: 'private-prompt', api_key: 'secret-value' }) }, '打开请求明细')
      },
    })
    const wrapper = await mountApp({ global: { stubs: { OperationsPane: RequestContextEmitter } } })
    await wrapper.findAll('button').find(button => button.text() === '打开请求明细')!.trigger('click'); await flushPromises()
    expect(window.location.hash).toBe('#/requests?page=1&status=success&detail=stored-request-id')
    expect(window.location.hash).not.toContain('private-prompt')
    expect(window.location.hash).not.toContain('secret-value')
  })

  it('个人筛选把白名单上下文写进 hash，并按本人端点查询、不携带 user_id', async () => {
    session = member
    window.history.replaceState({}, '', '#/myRequests?status=success&page=1')
    stubServer()
    const wrapper = await mountApp()
    await wrapper.get('[data-testid="filter-request-id"]').setValue('req-7')
    await wrapper.get('[data-testid="request-filters"]').trigger('submit'); await flushPromises()
    expect(window.location.hash).toBe('#/myRequests?page=1&status=success&request_id=req-7')
    const last = lastCall('/api/self/requests')!
    expect(params(last.url).get('offset')).toBe('0')
    expect(params(last.url).get('limit')).toBe('50')
    expect(params(last.url).get('request_id')).toBe('req-7')
    expect(last.url).not.toContain('user_id')
    expect(modelCalls()).toEqual([])
  })
})

describe('用户名入口与移动导航', () => {
  it('用户名入口使用登录后的会话 me.username，不是表单变量或显示名称', async () => {
    stubServer(url => url === '/api/auth/login' ? response({ user: member, csrf_token: 'csrf-token' }) : undefined)
    const wrapper = await mountApp()
    await submitLogin(wrapper, 'typed-into-login')
    expect(wrapper.find('.username-entry').text()).toBe('alice')
    expect(wrapper.text()).not.toContain('爱丽丝')
    expect(wrapper.find('.username-entry').attributes('aria-expanded')).toBe('false')
    await wrapper.find('.username-entry').trigger('click')
    const menu = wrapper.find('#account-menu')
    expect(menu.findAll('button').map(button => button.text())).toEqual(['账号中心', '我的密钥', '退出登录'])
    await menu.findAll('button').find(button => button.text() === '我的密钥')!.trigger('click'); await flushPromises()
    expect(window.location.hash).toBe('#/myKeys')
    expect(wrapper.find('#account-menu').exists()).toBe(false)
    await wrapper.find('.username-entry').trigger('click')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises()
    expect(wrapper.find('#account-menu').exists()).toBe(false)
  })

  it('移动导航按钮暴露 aria-expanded，并可用 Escape 收起', async () => {
    session = member
    stubServer()
    const wrapper = await mountApp()
    const toggle = wrapper.find('.nav-toggle')
    const aside = wrapper.find('aside#task-nav')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(aside.classes()).toContain('mobile-open')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises()
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(aside.classes()).not.toContain('mobile-open')
    expect(wrapper.findAll('aside nav button').every(button => !!button.attributes('aria-label'))).toBe(true)
  })
})

describe('加载、空状态、错误重试与重复保存保护', () => {
  it('读取时不报空，错误保留实际诊断元数据；重试只读取，不会发起模型调用', async () => {
    session = admin
    window.history.replaceState({}, '', '#/users')
    let mode: 'hold' | 'success' = 'hold'
    let release!: (value: unknown) => void
    stubServer(url => {
      if (url === '/api/admin/users') {
        if (mode === 'hold') return new Promise(resolve => { release = resolve })
        return response([{ id: 2, username: 'alice', display_name: '爱丽丝', role: 'user', active: true }])
      }
      return undefined
    })
    const wrapper = await mountApp()
    expect(wrapper.text()).toContain('正在加载网关…')
    expect(wrapper.text()).not.toContain('暂无记录')

    release(response({ error: { message: 'administrator store operation failed', code: 'admin_store_error', request_id: 'actual-store-error-id' } }, 500))
    await flushPromises()
    const alert = wrapper.find('.alert')
    expect(alert.text()).toContain('服务端存储操作失败')
    expect(alert.text()).toContain('admin_store_error')
    expect(alert.text()).toContain('actual-store-error-id')
    expect(alert.text()).toContain('重试读取')
    expect(wrapper.text()).not.toContain('暂无记录')

    mode = 'success'
    await alert.find('button').trigger('click'); await flushPromises()
    expect(wrapper.find('.table-row').text()).toContain('alice')
    expect(wrapper.find('.alert').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('正在加载网关…')
    expect(modelCalls()).toEqual([])
    expect(calls.every(call => call.method === 'GET' || call.url.startsWith('/api/auth/'))).toBe(true)
  })

  it('成功读取的空数组才显示空状态，而不是 loading 文案', async () => {
    session = admin
    window.history.replaceState({}, '', '#/users')
    stubServer()
    const wrapper = await mountApp()
    expect(wrapper.text()).toContain('暂无记录')
    expect(wrapper.text()).not.toContain('加载中…')
  })

  it('写入期间保存按钮禁用并阻止重复 POST', async () => {
    session = admin
    window.history.replaceState({}, '', '#/users')
    let release!: (value: unknown) => void
    stubServer((url, init) => url === '/api/admin/users' && (init.method || 'GET').toUpperCase() === 'POST'
      ? new Promise(resolve => { release = resolve }) : undefined)
    const wrapper = await mountApp()
    const create = wrapper.findAll('button').find(button => button.text().includes('新建'))!
    await create.trigger('click'); await flushPromises()
    await wrapper.findAll('label').find(label => label.text().startsWith('用户名'))!.find('input').setValue('new-user')
    await wrapper.findAll('label').find(label => label.text().startsWith('密码'))!.find('input').setValue('Secret!2345')
    const save = wrapper.findAll('button').find(button => button.text().trim() === '保存')!
    const form = wrapper.find('form.panel.form-panel')
    await form.trigger('submit')
    await form.trigger('submit')
    expect(calls.filter(call => call.method === 'POST' && call.url === '/api/admin/users')).toHaveLength(1)
    expect(wrapper.findAll('button').find(button => button.text() === '保存中…')!.attributes('disabled')).toBeDefined()
    release(response({ id: 9, username: 'new-user', active: true }, 201)); await flushPromises()
    expect(wrapper.find('.notice').text()).toContain('修改已保存')
  })
})
