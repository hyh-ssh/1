import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
const admin = { id: 1, username: 'admin', display_name: '管理员显示名', role: 'admin', password_login_enabled: true }
function stubServer(overrides: (url: string, init: RequestInit) => unknown = () => undefined) {
  vi.stubGlobal('fetch', vi.fn((url: string, init: RequestInit = {}) => {
    const method = (init.method || 'GET').toUpperCase()
    calls.push({ url, method, body: init.body ? JSON.parse(String(init.body)) : undefined })
    const overridden = overrides(url, init)
    if (overridden !== undefined) return Promise.resolve(overridden)
    if (url === '/api/auth/me') return Promise.resolve(session ? response({ user: session, csrf_token: 'csrf-token' }) : response({ error: { message: 'authentication required' } }, 401))
    if (url === '/api/auth/oidc/config') return Promise.resolve(response({ enabled: false }))
    if (url === '/api/auth/login') return Promise.resolve(response({ user: admin, csrf_token: 'csrf-token' }))
    if (url.startsWith('/api/admin/stats') || url.startsWith('/api/admin/runtime')) return Promise.resolve(response({}))
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
function setQuery(query: string) { window.history.replaceState({}, '', query ? `/?${query}` : '/') }
async function loginAs(wrapper: VueWrapper, inputUsername = 'typed-username') {
  const fields = wrapper.find('.login-card form').findAll('input')
  await fields[0]!.setValue(inputUsername); await fields[1]!.setValue('Secret!2345')
  await wrapper.find('.login-card form').trigger('submit'); await flushPromises()
}

beforeEach(() => {
  sessionStorage.clear(); setCSRFToken(''); calls = []; session = null
  window.history.replaceState({}, '', '#')
  vi.stubGlobal('scrollTo', vi.fn())
})
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  setCSRFToken(''); vi.unstubAllGlobals(); window.history.replaceState({}, '', '/'); window.history.replaceState({}, '', '#'); document.body.innerHTML = ''
})

describe('登录页 OIDC 入口', () => {
  it('未登录时读取 OIDC 配置；enabled 后展示固定跳转，密码登录和敏感信息边界不变', async () => {
    stubServer(url => url === '/api/auth/oidc/config' ? response({ enabled: true, client_secret: 'must-not-render' }) : undefined)
    const wrapper = await mountApp()
    expect(calls.some(call => call.url === '/api/auth/oidc/config')).toBe(true)
    const oauth = wrapper.find('.oauth-button')
    expect(oauth.exists()).toBe(true)
    expect(oauth.attributes('href')).toBe('/api/auth/oidc/start')
    expect(oauth.text()).toContain('使用企业账号登录')
    expect(wrapper.text()).toContain('或使用统一身份认证')
    expect(wrapper.find('.login-card form').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('must-not-render')
    expect(wrapper.text()).not.toContain('client_secret')
  })

  it('OIDC disabled 时不显示 OAuth 按钮，密码登录照常', async () => {
    stubServer(url => url === '/api/auth/oidc/config' ? response({ enabled: false }) : undefined)
    const wrapper = await mountApp()
    expect(wrapper.find('.oauth-button').exists()).toBe(false)
    expect(wrapper.find('.login-card form').exists()).toBe(true)
  })

  it('OIDC 配置读取失败时保持按钮隐藏，登录页仍可用', async () => {
    stubServer(url => url === '/api/auth/oidc/config' ? response({ error: { message: 'unavailable' } }, 500) : undefined)
    const wrapper = await mountApp()
    expect(wrapper.find('.oauth-button').exists()).toBe(false)
    expect(wrapper.find('.login-card form').exists()).toBe(true)
  })

  it('已登录时不再查询登录页 OIDC 配置，且入口显示会话 me.username', async () => {
    session = admin
    stubServer()
    const wrapper = await mountApp()
    expect(wrapper.find('.shell').exists()).toBe(true)
    expect(wrapper.find('.username-entry').text()).toBe('admin')
    expect(wrapper.text()).not.toContain('管理员显示名')
    expect(calls.some(call => call.url === '/api/auth/oidc/config')).toBe(false)
  })

  it('用户名入口取登录响应的 username，不使用表单输入值或 display_name', async () => {
    stubServer(url => url === '/api/auth/login' ? response({ user: admin, csrf_token: 'csrf-token' }) : undefined)
    const wrapper = await mountApp()
    await loginAs(wrapper, 'different-form-input')
    expect(wrapper.find('.username-entry').text()).toBe('admin')
    expect(wrapper.text()).not.toContain('different-form-input')
    expect(wrapper.text()).not.toContain('管理员显示名')
  })
})

describe('OIDC 回调错误提示', () => {
  it('access_denied 映射到固定中文取消提示', async () => {
    stubServer(); setQuery('oauth_error=access_denied')
    const wrapper = await mountApp()
    expect(wrapper.find('.error[role="alert"]').text()).toContain('取消 OAuth 授权')
  })

  it('未知 oauth_error 显示通用提示，不回显不可信错误码', async () => {
    stubServer(); setQuery('oauth_error=%3Cimg%20src%3Dx%20onerror%3Dalert(1)%3E')
    const wrapper = await mountApp()
    expect(wrapper.find('.error[role="alert"]').text()).toBe('OAuth 登录失败，请重试或改用用户名密码登录')
    expect(wrapper.text()).not.toContain('onerror')
  })

  it('不展示 code/state/token 等 OAuth 查询参数', async () => {
    stubServer(); setQuery('oauth_error=state_mismatch&code=SECRET-CODE&state=SECRET-STATE&token=SECRET-TOKEN')
    const wrapper = await mountApp()
    expect(wrapper.find('.error[role="alert"]').text()).toContain('状态校验失败')
    for (const value of ['SECRET-CODE', 'SECRET-STATE', 'SECRET-TOKEN']) expect(wrapper.text()).not.toContain(value)
  })

  it('没有 oauth_error 时不展示回调错误', async () => {
    stubServer(); setQuery('')
    const wrapper = await mountApp()
    expect(wrapper.find('.error[role="alert"]').exists()).toBe(false)
  })
})

describe('OIDC 成功回调', () => {
  it('根路径回调通过会话检查恢复控制台，不附带 OAuth 查询参数', async () => {
    session = admin
    stubServer(); setQuery('')
    const wrapper = await mountApp()
    expect(wrapper.find('.shell').exists()).toBe(true)
    expect(wrapper.find('.login-card').exists()).toBe(false)
    expect(location.search).toBe('')
    expect(location.hash).toBe('#/dashboard')
  })
})
