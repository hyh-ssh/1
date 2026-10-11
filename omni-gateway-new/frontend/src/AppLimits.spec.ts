import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import App from './App.vue'
import { setCSRFToken } from './api'

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => (typeof body === 'string' ? body : JSON.stringify(body)) }
}
type Call = { url: string; method: string; body: any }
let calls: Call[] = []
const wrappers: VueWrapper[] = []
const admin = { id: 1, username: 'admin', display_name: '管理员昵称', role: 'admin', password_login_enabled: true }
const users = [
  { id: 1, username: 'admin', display_name: '管理员昵称', role: 'admin', password_login_enabled: true },
  { id: 2, username: 'alice', display_name: '爱丽丝', role: 'user', password_login_enabled: true },
]
const keys = [{ id: 5, user_id: 2, name: 'alice-key', key_prefix: 'sk-llm-', key_suffix: '1234', active: true, model_allow: [] }]
const aliases = [{ id: 3, name: 'gpt-x', active: true }]
const rules = [{ id: 9, scope: 'user', scope_id: 2, model_id: '', rpm: 60, tpm: 0, concurrency: 0, active: true }]
function installServer() {
  vi.stubGlobal('fetch', vi.fn((url: string, init: RequestInit = {}) => {
    const method = (init.method || 'GET').toUpperCase()
    calls.push({ url, method, body: init.body ? JSON.parse(String(init.body)) : undefined })
    if (url === '/api/auth/me') return Promise.resolve(response({ user: admin, csrf_token: 'csrf-token' }))
    if (url.startsWith('/api/admin/users')) return Promise.resolve(response(users))
    if (url.startsWith('/api/admin/api-keys')) return Promise.resolve(response(keys))
    if (url.startsWith('/api/admin/aliases')) return Promise.resolve(response(aliases))
    if (url.startsWith('/api/admin/rate-limits')) return Promise.resolve(method === 'POST' ? response(rules[0], 201) : response(rules))
    if (url.startsWith('/api/admin/stats') || url.startsWith('/api/admin/runtime')) return Promise.resolve(response({}))
    if (url.startsWith('/api/admin/providers')) return Promise.resolve(response([]))
    if (url.startsWith('/readyz')) return Promise.resolve(response('ok'))
    return Promise.resolve(response([], 404))
  }))
}
async function openLimitsForm() {
  const wrapper = mount(App)
  wrappers.push(wrapper)
  await flushPromises()
  const limitNav = wrapper.findAll('aside nav button').find(button => button.text().includes('限流'))!
  await limitNav.trigger('click'); await flushPromises()
  const newButton = wrapper.findAll('button').find(button => button.text().includes('新建'))!
  await newButton.trigger('click'); await flushPromises()
  return wrapper
}
function labelWith(wrapper: VueWrapper, text: string) { return wrapper.findAll('label').find(label => label.text().startsWith(text)) }
function lastCall(url: string, method: string) { return [...calls].reverse().find(call => call.url === url && call.method === method) }

beforeEach(() => {
  sessionStorage.clear(); setCSRFToken(''); calls = []
  window.history.replaceState({}, '', '#')
  vi.stubGlobal('scrollTo', vi.fn())
  installServer()
})
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  setCSRFToken(''); vi.unstubAllGlobals(); document.body.innerHTML = ''
})

describe('管理端限流关联对象选择', () => {
  it('用户作用域按用户名而非显示名称构造下拉，并不依赖用户组或预算', async () => {
    const wrapper = await openLimitsForm()
    const target = labelWith(wrapper, '作用域对象')!
    const select = target.find('select')
    expect(select.exists()).toBe(true)
    expect(target.find('input[type="number"]').exists()).toBe(false)
    expect(select.findAll('option').map(option => option.text())).toContain('admin · #1')
    expect(select.findAll('option').map(option => option.text())).toContain('alice · #2')
    expect(select.text()).not.toContain('爱丽丝')
    expect(wrapper.text()).toContain('限流不依赖用户组或历史预算')
  })

  it('切到密钥作用域时以密钥名优先并同时说明所属用户名', async () => {
    const wrapper = await openLimitsForm()
    await labelWith(wrapper, '作用域')!.find('select').setValue('key'); await flushPromises()
    const options = labelWith(wrapper, '作用域对象')!.find('select').findAll('option').map(option => option.text())
    expect(options).toContain('alice-key · alice · #5')
  })

  it('模型作用域使用标签「限定模型」与别名名称，不显示原始作用域 ID', async () => {
    const wrapper = await openLimitsForm()
    await labelWith(wrapper, '作用域')!.find('select').setValue('model'); await flushPromises()
    expect(labelWith(wrapper, '作用域对象')).toBeUndefined()
    const modelSelect = labelWith(wrapper, '限定模型')!.find('select')
    expect(modelSelect.findAll('option').map(option => option.text())).toEqual(['全部模型', 'gpt-x'])
  })

  it('保存时传入选中的对象数值 ID，已有规则也按用户名显示', async () => {
    const wrapper = await openLimitsForm()
    await labelWith(wrapper, '作用域对象')!.find('select').setValue('2'); await flushPromises()
    const form = wrapper.find('form.panel.form-panel')
    await form.trigger('submit'); await flushPromises()
    const created = lastCall('/api/admin/rate-limits', 'POST')
    expect(created).toBeTruthy()
    expect(created!.body).toMatchObject({ scope: 'user', scope_id: 2, model_id: '' })
    expect(wrapper.text()).toContain('用户 alice')
  })

  it('只读列表不打开表单时，同样用用户名展示既有作用域规则', async () => {
    const wrapper = mount(App)
    wrappers.push(wrapper)
    await flushPromises()
    const limitNav = wrapper.findAll('aside nav button').find(button => button.text().includes('限流'))!
    await limitNav.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('用户 alice')
    expect(wrapper.text()).not.toContain('用户 #2')
  })
})
