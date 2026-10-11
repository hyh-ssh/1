import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import OidcProviderSettings from './OidcProviderSettings.vue'
import App from './App.vue'

// ---- helpers ---------------------------------------------------------------

function fakeResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  }
}

type Call = { url: string; method: string; body: any }
let calls: Call[] = []

type Handler = (url: string, method: string, init: RequestInit) => unknown

function stubServer(handler: Handler) {
  const fetchMock = vi.fn((url: string, init: RequestInit = {}) => {
    const method = (init.method || 'GET').toUpperCase()
    calls.push({ url, method, body: init.body ? JSON.parse(String(init.body)) : undefined })
    return Promise.resolve(handler(url, method, init))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const admin = { id: 1, username: 'admin', display_name: '管理员', role: 'admin' }
const member = { id: 2, username: 'alice', display_name: '普通用户', role: 'user' }

// 后端 adminOIDCProviderView 的镜像：secret 只有 has_client_secret，没有明文。
const configuredView = {
  enabled: true,
  issuer: 'https://idp.example.com',
  client_id: 'console-client',
  redirect_url: 'https://gateway.example.com/api/auth/oidc/callback',
  display_name: '企业统一登录',
  has_client_secret: true,
  ready: true,
  env_seed_applied: false,
  updated_at: '2026-09-26T10:00:00Z',
  updated_by: 1,
}

const emptyView = {
  enabled: false,
  issuer: '',
  client_id: '',
  redirect_url: '',
  display_name: '',
  has_client_secret: false,
  ready: false,
  env_seed_applied: true,
  updated_at: null,
  updated_by: 0,
}

// 表单输入顺序：0 启用开关、1 Issuer、2 Client ID、3 回调地址、4 Client Secret、5 显示名称。
function inputs(wrapper: VueWrapper) {
  return wrapper.findAll('input')
}

function buttonByText(wrapper: VueWrapper, text: string) {
  const button = wrapper.findAll('button').find(item => item.text().includes(text))
  if (!button) throw new Error(`button not found: ${text}`)
  return button
}

async function mountSettings(handler: Handler) {
  stubServer(handler)
  const wrapper = mount(OidcProviderSettings)
  await flushPromises()
  return wrapper
}

function putCall() {
  const call = calls.find(item => item.method === 'PUT' && item.url === '/api/admin/oidc-provider')
  if (!call) throw new Error('no PUT to /api/admin/oidc-provider')
  return call
}

beforeEach(() => {
  sessionStorage.clear()
  calls = []
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// ---- 状态渲染 ---------------------------------------------------------------

describe('主 OIDC Provider 已配置/未配置两态', () => {
  it('已配置时展示 issuer、client_id、回调地址与显示名称，secret 只显示「已设置」', async () => {
    const wrapper = await mountSettings(() => fakeResponse(configuredView))

    expect(calls[0]).toEqual({ url: '/api/admin/oidc-provider', method: 'GET', body: undefined })
    expect(wrapper.text()).toContain('已配置')
    expect(wrapper.text()).toContain('已启用')
    expect(wrapper.text()).toContain('https://idp.example.com')
    expect(wrapper.text()).toContain('console-client')
    expect(wrapper.text()).toContain('https://gateway.example.com/api/auth/oidc/callback')
    expect(wrapper.text()).toContain('企业统一登录')
    expect(wrapper.text()).toContain('已设置')

    // 表单预填非密钥字段；secret 永远是空的 password 输入框（明文不回显）。
    const fields = inputs(wrapper)
    expect(fields[0].element).toHaveProperty('checked', true)
    expect((fields[1].element as HTMLInputElement).value).toBe('https://idp.example.com')
    expect((fields[2].element as HTMLInputElement).value).toBe('console-client')
    expect((fields[3].element as HTMLInputElement).value).toBe('https://gateway.example.com/api/auth/oidc/callback')
    expect((fields[4].element as HTMLInputElement).value).toBe('')
    expect((fields[4].element as HTMLInputElement).type).toBe('password')
    expect(fields[4].attributes('placeholder')).toContain('留空表示不修改')
    expect((fields[5].element as HTMLInputElement).value).toBe('企业统一登录')
  })

  it('未配置时展示「未配置 / 未设置」，表单为空且开关关闭', async () => {
    const wrapper = await mountSettings(() => fakeResponse(emptyView))

    expect(wrapper.text()).toContain('未配置')
    expect(wrapper.text()).toContain('未启用')
    expect(wrapper.text()).toContain('未设置')

    const fields = inputs(wrapper)
    expect(fields[0].element).toHaveProperty('checked', false)
    for (const index of [1, 2, 3, 4, 5]) {
      expect((fields[index].element as HTMLInputElement).value).toBe('')
    }
    expect(fields[4].attributes('placeholder')).toContain('未设置，请填写')
  })

  it('加载失败时给出中文提示与重试入口', async () => {
    const wrapper = await mountSettings(() => fakeResponse({ error: { message: 'administrator store operation failed' } }, 500))

    expect(wrapper.find('.alert').text()).toContain('服务端存储操作失败')
    expect(buttonByText(wrapper, '重试').exists()).toBe(true)
  })
})

// ---- 保存 -------------------------------------------------------------------

describe('主 OIDC Provider 保存', () => {
  it('保存时把表单字段 PUT 到管理接口，带 secret 时载荷包含 client_secret', async () => {
    stubServer((url, method) => {
      if (method === 'GET') return fakeResponse(emptyView)
      if (method === 'PUT') return fakeResponse({ ...configuredView })
      return fakeResponse({}, 404)
    })
    const wrapper = mount(OidcProviderSettings)
    await flushPromises()

    await inputs(wrapper)[0].setValue(true)
    await inputs(wrapper)[1].setValue('https://idp.example.com')
    await inputs(wrapper)[2].setValue('console-client')
    await inputs(wrapper)[3].setValue('https://gateway.example.com/api/auth/oidc/callback')
    await inputs(wrapper)[4].setValue('s3cret-value')
    await inputs(wrapper)[5].setValue('企业统一登录')
    await buttonByText(wrapper, '保存').trigger('click')
    await flushPromises()

    expect(putCall().body).toEqual({
      enabled: true,
      issuer: 'https://idp.example.com',
      client_id: 'console-client',
      redirect_url: 'https://gateway.example.com/api/auth/oidc/callback',
      display_name: '企业统一登录',
      client_secret: 's3cret-value',
    })
    // 保存成功后用服务端视图回填，并给出成功提示。
    expect(wrapper.find('.notice').text()).toContain('配置已保存')
    expect(wrapper.text()).toContain('https://idp.example.com')
    expect((inputs(wrapper)[4].element as HTMLInputElement).value).toBe('')
  })

  it('secret 留空时不出现在 PUT 载荷中（保留已存密钥）', async () => {
    stubServer((url, method) => {
      if (method === 'GET') return fakeResponse(configuredView)
      if (method === 'PUT') return fakeResponse(configuredView)
      return fakeResponse({}, 404)
    })
    const wrapper = mount(OidcProviderSettings)
    await flushPromises()

    await inputs(wrapper)[5].setValue('新的显示名称')
    await buttonByText(wrapper, '保存').trigger('click')
    await flushPromises()

    const body = putCall().body
    expect(Object.prototype.hasOwnProperty.call(body, 'client_secret')).toBe(false)
    expect(Object.keys(body).sort()).toEqual(['client_id', 'display_name', 'enabled', 'issuer', 'redirect_url'])
    expect(body.display_name).toBe('新的显示名称')
    // 没有 secret 进载荷，服务端视图里的「已设置」保持不变。
    expect(wrapper.text()).toContain('已设置')
  })

  it('保存中禁用按钮并忽略重复提交', async () => {
    let release: (value: unknown) => void = () => {}
    stubServer((url, method) => {
      if (method === 'GET') return fakeResponse(emptyView)
      if (method === 'PUT') return new Promise(resolve => { release = resolve })
      return fakeResponse({}, 404)
    })
    const wrapper = mount(OidcProviderSettings)
    await flushPromises()

    const saveButton = buttonByText(wrapper, '保存')
    await saveButton.trigger('click')
    await saveButton.trigger('click')
    await flushPromises()

    expect(saveButton.text()).toBe('保存中…')
    expect(saveButton.attributes('disabled')).toBeDefined()
    expect(calls.filter(call => call.method === 'PUT')).toHaveLength(1)

    release(fakeResponse(configuredView))
    await flushPromises()
    expect(buttonByText(wrapper, '保存').attributes('disabled')).toBeUndefined()
  })

  it('后端校验错误落成中文可读文案（含字段中文标签）', async () => {
    stubServer((url, method) => {
      if (method === 'GET') return fakeResponse(emptyView)
      if (method === 'PUT') {
        return fakeResponse({ error: { message: 'enabled OIDC provider requires issuer, client_id, client_secret, redirect_url' } }, 400)
      }
      return fakeResponse({}, 404)
    })
    const wrapper = mount(OidcProviderSettings)
    await flushPromises()

    await buttonByText(wrapper, '保存').trigger('click')
    await flushPromises()

    const alert = wrapper.find('.alert[role="alert"]')
    expect(alert.text()).toContain('启用主 OIDC Provider 前必须填写')
    expect(alert.text()).toContain('issuer（签发方）')
    expect(alert.text()).toContain('client_id（客户端 ID）')
    expect(alert.text()).toContain('client_secret（客户端密钥）')
    expect(alert.text()).toContain('redirect_url（回调地址）')
    expect(alert.text()).not.toContain('enabled OIDC provider requires')
    // 失败后按钮恢复可用，可以修改后重试。
    expect(buttonByText(wrapper, '保存').attributes('disabled')).toBeUndefined()
  })

  it('非法 issuer 的后端校验文案也被本地化', async () => {
    const wrapper = await mountSettings((url, method) => {
      if (method === 'GET') return fakeResponse(emptyView)
      if (method === 'PUT') return fakeResponse({ error: { message: 'issuer must be an absolute URL' } }, 400)
      return fakeResponse({}, 404)
    })

    await buttonByText(wrapper, '保存').trigger('click')
    await flushPromises()

    expect(wrapper.find('.alert[role="alert"]').text()).toContain('Issuer 必须是绝对 URL')
  })
})

// ---- 清空 -------------------------------------------------------------------

describe('主 OIDC Provider 清空', () => {
  it('「清空配置」发 DELETE 并重新读取服务端状态', async () => {
    let cleared = false
    stubServer((url, method) => {
      if (method === 'DELETE') { cleared = true; return fakeResponse(null, 204) }
      if (method === 'GET') return fakeResponse(cleared ? emptyView : configuredView)
      return fakeResponse({}, 404)
    })
    vi.stubGlobal('confirm', vi.fn(() => true))

    const wrapper = mount(OidcProviderSettings)
    await flushPromises()
    expect(wrapper.text()).toContain('已配置')

    await buttonByText(wrapper, '清空配置').trigger('click')
    await flushPromises()

    expect(calls.some(call => call.method === 'DELETE' && call.url === '/api/admin/oidc-provider')).toBe(true)
    // 清空后状态回到未配置，并给出提示；不是本地猜测。
    expect(wrapper.text()).toContain('未配置')
    expect(wrapper.find('.notice').text()).toContain('配置已清空')
    expect((inputs(wrapper)[0].element as HTMLInputElement).checked).toBe(false)
  })

  it('取消确认时不发 DELETE', async () => {
    vi.stubGlobal('confirm', vi.fn(() => false))

    const wrapper = await mountSettings(() => fakeResponse(configuredView))
    await buttonByText(wrapper, '清空配置').trigger('click')
    await flushPromises()

    expect(calls.some(call => call.method === 'DELETE')).toBe(false)
  })
})

// ---- 管理员范围 -------------------------------------------------------------

describe('设置区域仅管理员可见可操作', () => {
  it('管理员侧栏出现「设置」，点击后加载并渲染主 OIDC Provider 表单', async () => {
    stubServer((url) => {
      if (url === '/api/auth/me') return fakeResponse({ user: admin, csrf_token: 'csrf-token' })
      if (url.startsWith('/api/admin/oidc-provider')) return fakeResponse(configuredView)
      if (url.startsWith('/api/admin/stats') || url.startsWith('/api/admin/runtime')) return fakeResponse({})
      if (url.startsWith('/api/admin/providers')) return fakeResponse([])
      if (url === '/readyz') return fakeResponse({ ok: true })
      return fakeResponse({}, 404)
    })

    const wrapper = mount(App)
    await flushPromises()
    expect(wrapper.find('.shell').exists()).toBe(true)

    const settingsNav = wrapper.findAll('aside nav button').find(item => item.text().includes('设置'))
    expect(settingsNav).toBeTruthy()
    await settingsNav!.trigger('click')
    await flushPromises()

    expect(calls.some(call => call.url === '/api/admin/oidc-provider' && call.method === 'GET')).toBe(true)
    expect(wrapper.text()).toContain('主 OIDC Provider')
    expect(wrapper.text()).toContain('企业统一登录')
    // 表单可操作，且 secret 输入框不回显（值必须为空）。
    const form = wrapper.find('.form-panel')
    expect((form.findAll('input')[4].element as HTMLInputElement).value).toBe('')
    expect(form.findAll('input')[4].attributes('disabled')).toBeUndefined()
    expect(buttonByText(wrapper, '保存').attributes('disabled')).toBeUndefined()
  })

  it('普通用户看不到「设置」，也不会请求管理端 OIDC 接口', async () => {
    stubServer((url) => {
      if (url === '/api/auth/me') return fakeResponse({ user: member, csrf_token: 'csrf-token' })
      if (url === '/api/self/summary') return fakeResponse({ usage: {}, budgets: [], limits: [] })
      if (url.startsWith('/api/admin/')) return fakeResponse({ error: { message: 'administrator privileges required' } }, 403)
      return fakeResponse({}, 404)
    })

    const wrapper = mount(App)
    await flushPromises()

    expect(wrapper.find('.shell').exists()).toBe(true)
    expect(wrapper.findAll('aside nav button').some(item => item.text().includes('设置'))).toBe(false)
    expect(wrapper.text()).not.toContain('主 OIDC Provider')
    expect(calls.some(call => call.url === '/api/admin/oidc-provider')).toBe(false)
    expect(calls.some(call => call.url.startsWith('/api/admin/'))).toBe(false)
    // 页面上只剩账号中心的「修改密码」输入框，没有任何 OIDC Provider 字段。
    expect(wrapper.text()).not.toContain('Client Secret')
    expect(wrapper.text()).not.toContain('Client ID')
  })
})
