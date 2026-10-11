import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

enableAutoUnmount(afterEach)
afterEach(() => vi.restoreAllMocks())

// The component imports `api` from './api'; replace it with a controllable stub.
const { resourceMock } = vi.hoisted(() => ({
  resourceMock: vi.fn<(name: string, init?: RequestInit) => Promise<{ data?: unknown; error?: string }>>(),
}))

vi.mock('./api', () => ({ api: { resource: resourceMock } }))

import OperationsPane from './OperationsPane.vue'

// ---- fixtures --------------------------------------------------------------

const auditRows = [
  {
    id: 11,
    actor_id: 1,
    actor_name: 'admin',
    action: 'POST',
    resource: 'api-keys',
    detail: 'admin API mutation',
    ip: '127.0.0.1',
    created_at: '2026-09-18T09:17:48.202299+08:00',
  },
  {
    id: 10,
    actor_id: 2,
    actor_name: '',
    action: 'PATCH',
    resource: 'prices/3',
    detail: '',
    ip: '10.0.0.8',
    created_at: '2026-09-18T08:00:00.000000+08:00',
  },
]

const requestsRow = {
  id: 7,
  created_at: '2026-09-18T09:20:00.000000+08:00',
  api: 'chat_completions',
  status: 'success',
  total_latency_ms: 12,
  cost: 0,
}

const requestsDetail = {
  request: { id: 7, user_id: 5, api_key_id: 3, total_latency_ms: 12, ttft_ms: 4, credits: 0, usage: {} },
  attempts: [
    {
      settlement_id: 's1',
      provider_model: 'missing-model',
      status: 'success',
      status_code: 200,
      latency_ms: 12,
      cost: 0,
      settlement_error: 'unpriced',
      error_summary: '',
    },
  ],
  ledger: [],
}

beforeEach(() => {
  resourceMock.mockReset()
  resourceMock.mockImplementation(async (name: string) => {
    if (name.startsWith('admin/audit')) return { data: auditRows }
    return { data: [] }
  })
})

// ---- audit view ------------------------------------------------------------

describe('OperationsPane audit view', () => {
  it('renders UTC time, actor, action, resource and ip with explicit unknown legacy results', async () => {
    const wrapper = mount(OperationsPane, { props: { view: 'audit' } })
    await flushPromises()

    // audit must be fetched from the read-only admin/audit endpoint
    expect(resourceMock.mock.calls.some(([name]) => String(name).startsWith('admin/audit'))).toBe(true)
    expect(wrapper.find('h2').text()).toBe('审计日志')

    const rows = wrapper.findAll('.op-row')
    expect(rows).toHaveLength(2)

    const first = rows[0]!.text()
    expect(first).toContain('admin')
    expect(first).toContain('POST')
    expect(first).toContain('api-keys')
    expect(first).toContain('127.0.0.1')
    expect(first).toContain('2026-09-18 01:17:48.202 UTC')
    expect(first).toContain('未知（历史记录未记录结果）')
    expect(first).not.toContain('admin API mutation')

    // actor_name missing -> fall back to 用户 <actor_id>; empty detail renders nothing
    const second = rows[1]!.text()
    expect(second).toContain('用户 2')
    expect(second).toContain('PATCH')
    expect(second).toContain('prices/3')
    expect(second).toContain('10.0.0.8')
    expect(second).toContain('2026-09-18 00:00:00.000 UTC')

    wrapper.unmount()
  })

  it('renders no edit, toggle, delete or detail buttons for audit rows', async () => {
    const wrapper = mount(OperationsPane, { props: { view: 'audit' } })
    await flushPromises()

    expect(wrapper.findAll('.op-row').length).toBeGreaterThan(0)
    // the audit list is read-only: not a single per-row button
    expect(wrapper.findAll('.op-row button')).toHaveLength(0)

    // no mutation label may appear on a row (the generic list renders 编辑/停用/删除/请求明细)
    for (const row of wrapper.findAll('.op-row'))
      for (const label of ['编辑', '停用', '启用', '删除', '请求明细'])
        expect(row.text(), `audit row must not render ${label}`).not.toContain(label)

    // and the create button is gone too (audit has no create form / no POST endpoint)
    const text = wrapper.text()
    for (const label of ['+ 新建', '验证连接', '发现模型', '配置路由', '模型目录'])
      expect(text, `audit view must not render ${label}`).not.toContain(label)

    // the toolbar keeps only the refresh action
    expect(wrapper.findAll('.toolbar button').map((button) => button.text())).toEqual(['刷新'])

    wrapper.unmount()
  })
})

// ---- settlement hint -------------------------------------------------------

describe('settlementHint', () => {
  it('explains an unpriced settlement error and keeps unknown values visible', () => {
    const wrapper = mount(OperationsPane, { props: { view: 'audit' } })
    const hint = (wrapper.vm as unknown as { settlementHint: (value: string) => string }).settlementHint

    expect(typeof hint).toBe('function')
    expect(hint('unpriced')).toContain('价格版本')
    expect(hint('unpriced')).toContain('未计费')
    expect(hint('unpriced')).toContain('零费率')
    expect(hint('unpriced')).toContain('免费')

    const unknown = hint('ledger_conflict')
    expect(unknown.length).toBeGreaterThan(0)
    expect(unknown).toContain('ledger_conflict')

    wrapper.unmount()
  })

  it('shows the unpriced hint on the upstream attempt row of a request', async () => {
    resourceMock.mockImplementation(async (name: string) => {
      if (name.startsWith('admin/requests/')) return { data: requestsDetail }
      if (name.startsWith('admin/requests')) return { data: [requestsRow] }
      return { data: [] }
    })

    const wrapper = mount(OperationsPane, { props: { view: 'requests' } })
    await flushPromises()

    const detailButton = wrapper.findAll('button').find((button) => button.text() === '请求明细')
    expect(detailButton).toBeDefined()
    await detailButton!.trigger('click')
    await flushPromises()

    const attempt = wrapper.find('.route-row')
    expect(attempt.text()).toContain('missing-model')
    expect(attempt.text()).toContain('未配置价格版本')
    expect(attempt.text()).toContain('价格')
    // the raw machine value is no longer shown alone
    expect(attempt.text()).not.toContain('结算失败，需核对账本')

    wrapper.unmount()
  })
  it('在零金额时仍完整显示账本数量和价格版本，不推断请求为免费', async () => {
    const freeDetail = {
      request: { id: 8, user_id: 5, api_key_id: 3, total_latency_ms: 25, ttft_ms: 6, credits: '0.000', usage: { meters: { input_tokens: 128, total: 128 } } },
      attempts: [{ settlement_id: 'settlement-free', provider_model: 'free-model', status: 'success', status_code: 200, latency_ms: 25, cost: '0.000000', settlement_error: '', error_summary: '' }],
      ledger: [{ id: 81, settlement_id: 'settlement-free', meter: 'input_tokens', quantity: '128', cost: '0.000000', credits: '0.000', price_version: 4, estimated: false }],
    }
    resourceMock.mockImplementation(async (name: string) => {
      if (name.startsWith('admin/requests/')) return { data: freeDetail }
      if (name.startsWith('admin/requests')) return { data: [{ ...requestsRow, id: 8, model_alias: 'free-model' }] }
      return { data: [] }
    })

    const wrapper = mount(OperationsPane, { props: { view: 'requests' } })
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '请求明细')!.trigger('click')
    await flushPromises()

    const ledgerRow = wrapper.findAll('.request-drawer .scroll tbody tr')[0]!
    expect(ledgerRow.text()).toContain('input_tokens')
    expect(ledgerRow.text()).toContain('128')
    expect(ledgerRow.text()).toContain('0.000000')
    expect(ledgerRow.text()).toContain('0.000')
    expect(ledgerRow.text()).toContain('4')
    expect(wrapper.find('.request-facts').text()).toContain('未记录（历史记录）')
    expect(wrapper.find('.request-facts').text()).not.toContain('免费（显式零费率）')
    wrapper.unmount()
  })
})

// ---- provider health reasons ------------------------------------------------

describe('OperationsPane provider view', () => {
  it('renders the localized probe reason instead of the raw gateway text', async () => {
    resourceMock.mockImplementation(async (name: string) => {
      if (name.startsWith('admin/providers')) {
        return {
          data: [
            {
              id: 1,
              name: 'deepseek',
              type: 'openai_compatible',
              base_url: 'http://127.0.0.1:9/v1',
              active: true,
              health_status: 'cooldown',
              cooldown_until: '2099-01-01T00:00:00Z',
              last_error: 'provider address is blocked by the network policy',
            },
          ],
        }
      }
      return { data: [] }
    })

    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()

    expect(wrapper.text()).toContain('上游地址被网络策略拦截')
    expect(wrapper.text()).not.toContain('blocked by the network policy')

    wrapper.unmount()
  })

  // The console used to hide the opt-in inside a provider form that the router
  // never renders, so an operator had no way to enable /v1/responses and every
  // call was refused. Saving the provider form must always carry the flag.
  it('sends the Responses API opt-in when the provider form is saved', async () => {
    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()

    await wrapper.find('button.primary.compact').trigger('click')
    await flushPromises()

    const optIn = wrapper.findAll('label').find(label => label.text().includes('支持 Responses API'))
    expect(optIn).toBeTruthy()
    await optIn!.find('input[type="checkbox"]').setValue(true)

    const nameField = wrapper.findAll('label').find(label => label.text().includes('名称'))
    await nameField!.find('input').setValue('probe-provider')

    await wrapper.find('form.form-panel').trigger('submit')
    await flushPromises()

    const saveCall = resourceMock.mock.calls.find(([name, init]) => name === 'admin/providers' && init?.method === 'POST')
    expect(saveCall).toBeTruthy()
    expect(JSON.parse(String(saveCall![1]!.body))).toMatchObject({ name: 'probe-provider', supports_responses: true })

    wrapper.unmount()
  })

  it('marks each provider row with its Responses API capability', async () => {
    resourceMock.mockImplementation(async (name: string) => {
      if (name.startsWith('admin/providers')) {
        return {
          data: [
            { id: 1, name: 'vllm', type: 'openai_compatible', base_url: 'http://172.20.88.32:8000/v1', active: true, supports_responses: false },
            { id: 2, name: 'openai', type: 'openai_compatible', base_url: 'https://api.openai.com/v1', active: true, supports_responses: true },
          ],
        }
      }
      return { data: [] }
    })

    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()

    // 逐行断言，而不是数出现次数：「不支持 Responses API」本身包含「支持
    // Responses API」子串，按次数断言的话，一个恒定输出的徽标也能通过。
    const rows = wrapper.findAll('.op-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('不支持 Responses API')
    expect(rows[1].text()).toContain('支持 Responses API')
    expect(rows[1].text()).not.toContain('不支持')

    wrapper.unmount()
  })

  it('sends the opt-in as false when an existing provider is unticked', async () => {
    resourceMock.mockImplementation(async (name: string) => {
      if (name.startsWith('admin/providers')) {
        return {
          data: [
            { id: 2, name: 'openai', type: 'openai_compatible', base_url: 'https://api.openai.com/v1', active: true, supports_responses: true },
          ],
        }
      }
      return { data: [] }
    })

    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()

    const editButton = wrapper.findAll('.op-row button').find(button => button.text() === '编辑')
    await editButton!.trigger('click')
    await flushPromises()

    const optIn = wrapper.findAll('label').find(label => label.text().includes('支持 Responses API'))
    expect((optIn!.find('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true)
    await optIn!.find('input[type="checkbox"]').setValue(false)

    await wrapper.find('form.form-panel').trigger('submit')
    await flushPromises()

    // 关闭开关必须显式发 false，否则后端会保留原值、供应商继续被当成支持的。
    const saveCall = resourceMock.mock.calls.find(([name, init]) => name === 'admin/providers/2' && init?.method === 'PATCH')
    expect(saveCall).toBeTruthy()
    expect(JSON.parse(String(saveCall![1]!.body))).toMatchObject({ supports_responses: false })

    wrapper.unmount()
  })
})

describe('OperationsPane provider compatibility profile', () => {
  it('submits generic for a new provider when no profile is selected explicitly', async () => {
    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()
    await wrapper.find('button.primary.compact').trigger('click')
    await flushPromises()
    await wrapper.findAll('label').find(label => label.text().includes('名称'))!.find('input').setValue('Generic')
    await wrapper.find('form.form-panel').trigger('submit')
    await flushPromises()
    const save = resourceMock.mock.calls.find(([name, init]) => name === 'admin/providers' && init?.method === 'POST')
    expect(save).toBeTruthy()
    expect(JSON.parse(String(save![1]!.body))).toMatchObject({ compatibility_profile: 'generic' })
    wrapper.unmount()
  })

  it('submits DeepSeek for OpenAI-compatible providers', async () => {
    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()
    await wrapper.find('button.primary.compact').trigger('click')
    await flushPromises()

    const profile = wrapper.findAll('label').find(label => label.text().includes('兼容配置'))!.find('select')
    expect((profile.element as HTMLSelectElement).value).toBe('generic')
    await profile.setValue('deepseek')
    await wrapper.findAll('label').find(label => label.text().includes('名称'))!.find('input').setValue('DeepSeek')
    await wrapper.find('form.form-panel').trigger('submit')
    await flushPromises()

    const save = resourceMock.mock.calls.find(([name, init]) => name === 'admin/providers' && init?.method === 'POST')
    expect(save).toBeTruthy()
    expect(JSON.parse(String(save![1]!.body))).toMatchObject({ type: 'openai_compatible', compatibility_profile: 'deepseek' })
    wrapper.unmount()
  })

  it('displays profiles, edits an existing provider and blocks DeepSeek with other protocols', async () => {
    resourceMock.mockImplementation(async (name: string) => {
      if (name.startsWith('admin/providers')) return { data: [
        { id: 1, name: 'DeepSeek', type: 'openai_compatible', compatibility_profile: 'deepseek', base_url: 'https://api.deepseek.com/v1', active: true },
        { id: 2, name: 'generic', type: 'anthropic', compatibility_profile: 'generic', base_url: 'https://api.example.com', active: true },
      ] }
      return { data: [] }
    })
    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()
    const rows = wrapper.findAll('.op-row')
    expect(rows[0]!.text()).toContain('兼容配置：DeepSeek')
    expect(rows[1]!.text()).toContain('兼容配置：通用')
    await rows[0]!.findAll('button').find(button => button.text() === '编辑')!.trigger('click')
    await flushPromises()

    const profile = wrapper.findAll('label').find(label => label.text().includes('兼容配置'))!.find('select')
    const type = wrapper.findAll('label').find(label => label.text().includes('协议类型'))!.find('select')
    expect((profile.element as HTMLSelectElement).value).toBe('deepseek')
    await type.setValue('anthropic')
    expect(profile.find('option[value="deepseek"]').attributes('disabled')).toBeDefined()
    await wrapper.find('form.form-panel').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('DeepSeek')
    expect(resourceMock.mock.calls.some(([name, init]) => name === 'admin/providers/1' && init?.method === 'PATCH')).toBe(false)

    await profile.setValue('generic')
    await wrapper.find('form.form-panel').trigger('submit')
    await flushPromises()
    const save = resourceMock.mock.calls.find(([name, init]) => name === 'admin/providers/1' && init?.method === 'PATCH')
    expect(save).toBeTruthy()
    expect(JSON.parse(String(save![1]!.body))).toMatchObject({ type: 'anthropic', compatibility_profile: 'generic' })
    wrapper.unmount()
  })
})

function button(wrapper: any, label: string, selector = 'button') {
  return wrapper.findAll(selector).find((item: any) => item.text() === label)!
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const fullPage = () => Array.from({ length: 50 }, (_, index) => ({ ...requestsRow, id: `request-${index}`, model_alias: 'chat-model', user_id: 5, api_key_id: 3, settlement_status: 'free' }))
const listQueries = (view = 'requests') => resourceMock.mock.calls.filter(([name, init]) => name.startsWith(`admin/${view}?`) && !init?.method).map(([name]) => new URLSearchParams(name.split('?')[1]))
const mutations = () => resourceMock.mock.calls.filter(([, init]) => init?.method && init.method !== 'GET')

describe('Operations request filters, settlement and context', () => {
  it('distinguishes explicit free, credits-free, unpriced, failed and legacy zero amounts in compact rows', async () => {
    resourceMock.mockResolvedValue({ data: [
      { ...requestsRow, id: 'free', settlement_status: 'free' },
      { ...requestsRow, id: 'credits-free', cost: '0.001', settlement_status: 'credits_free' },
      { ...requestsRow, id: 'missing', settlement_status: 'unpriced' },
      { ...requestsRow, id: 'settlement-failure', settlement_status: 'failed' },
      { ...requestsRow, id: 'call-failure', status: 'error', status_code: 502, settlement_status: 'none', error_summary: '上游调用失败' },
      { ...requestsRow, id: 'legacy-zero' },
    ] })
    const wrapper = mount(OperationsPane, { props: { view: 'requests' } })
    await flushPromises()
    const rows = wrapper.findAll('.request-table tbody tr')
    expect(rows).toHaveLength(6)
    expect(rows[0]!.text()).toContain('免费（显式零费率）')
    expect(rows[1]!.text()).toContain('免积分（成本独立计量）')
    expect(rows[2]!.text()).toContain('未定价（非免费）')
    expect(rows[3]!.text()).toContain('结算失败')
    expect(rows[4]!.text()).toContain('失败HTTP 502')
    expect(rows[5]!.text()).toContain('未记录（历史记录）')
    expect(rows[5]!.text()).not.toContain('免费')
    expect(rows[0]!.text()).toContain('UTC')
  })

  it('restores all safe filters, pages by 50 and emits a full context without arbitrary or secret fields', async () => {
    resourceMock.mockResolvedValue({ data: fullPage() })
    const context = { page: '3', from: '2026-10-03T00:00:00Z', to: '2026-10-03T23:59:59Z', request_id: 'gateway-exact-id', user_id: '5', api_key_id: '3', model: 'chat-model', api: 'chat_completions', provider_id: '2', status: 'success', settlement_status: 'free', model_group_id: '4', secret: 'do-not-retain', prompt: 'do-not-retain' }
    const wrapper = mount(OperationsPane, { props: { view: 'requests', context } })
    await flushPromises()
    const query = listQueries()[0]!
    expect(query.get('offset')).toBe('100')
    expect(query.get('limit')).toBe('50')
    for (const field of ['request_id', 'user_id', 'api_key_id', 'model', 'api', 'provider_id', 'status', 'settlement_status', 'model_group_id']) expect(query.get(field)).toBe((context as Record<string, string>)[field])
    expect(query.get('from')).toBe('2026-10-03T00:00:00.000Z')
    expect((wrapper.find('[name="request_id"]').element as HTMLInputElement).value).toBe('gateway-exact-id')
    await button(wrapper, '下一页').trigger('click'); await flushPromises()
    expect(listQueries().slice(-1)[0]!.get('offset')).toBe('150')
    const emitted = wrapper.emitted('context-change')!.slice(-1)[0]![0] as Record<string, string>
    expect(emitted).toMatchObject({ page: '4', request_id: 'gateway-exact-id', model_group_id: '4' })
    expect(emitted).not.toHaveProperty('secret'); expect(emitted).not.toHaveProperty('prompt')
    await wrapper.setProps({ context: emitted }); await flushPromises()
    expect(listQueries()).toHaveLength(2)
    await wrapper.find('[name="request_id"]').setValue('new-exact-id')
    await wrapper.find('[name="from"]').setValue('2026-10-03T08:00:00+08:00')
    await wrapper.find('form.filters').trigger('submit'); await flushPromises()
    expect(listQueries().slice(-1)[0]!.get('offset')).toBe('0')
    expect(listQueries().slice(-1)[0]!.get('request_id')).toBe('new-exact-id')
    expect(listQueries().slice(-1)[0]!.get('from')).toBe('2026-10-03T00:00:00.000Z')
    expect(wrapper.emitted('context-change')!.slice(-1)[0]![0]).toMatchObject({ page: '1', request_id: 'new-exact-id' })
  })

  it('opens a modal immediately without reloading/remounting the list, traps focus, and returns to the same row on Escape', async () => {
    const pending = deferred<{ data: unknown }>()
    resourceMock.mockImplementation(async name => name === 'admin/requests/request-0' ? pending.promise : { data: fullPage() })
    const wrapper = mount(OperationsPane, { attachTo: document.body, props: { view: 'requests', context: { page: '2', status: 'success' } } })
    await flushPromises()
    const list = wrapper.find('.request-list').element as HTMLElement
    list.scrollTop = 123; list.scrollLeft = 45
    const trigger = wrapper.find('.request-action button')
    await trigger.trigger('click'); await nextTick()
    const modal = wrapper.find('[role="dialog"]')
    expect(modal.attributes('aria-modal')).toBe('true')
    expect(modal.find('[aria-busy="true"]').text()).toContain('正在加载')
    expect(document.activeElement).toBe(wrapper.find('[aria-label="关闭请求详情"]').element)
    expect(wrapper.find('.request-list').element).toBe(list)
    const opened = wrapper.emitted('context-change')!.slice(-1)[0]![0] as Record<string, string>
    expect(opened).toEqual({ page: '2', status: 'success', detail: 'request-0' })
    await wrapper.setProps({ context: opened }); await flushPromises()
    expect(listQueries()).toHaveLength(1)
    pending.resolve({ data: { ...requestsDetail, request: { ...requestsDetail.request, id: 'request-0' } } }); await flushPromises()
    const focusable = wrapper.findAll('.request-drawer button:not([disabled]), .request-drawer input:not([disabled])')
    const last = focusable[focusable.length - 1]!.element as HTMLElement
    last.focus(); document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(wrapper.find('[aria-label="关闭请求详情"]').element)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(last)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await nextTick()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)
    expect(list.scrollTop).toBe(123); expect(list.scrollLeft).toBe(45)
    expect(wrapper.emitted('context-change')!.slice(-1)[0]![0]).toEqual({ page: '2', status: 'success' })
    expect(wrapper.find('.pager').text()).toContain('第 2 页')
    expect(listQueries()).toHaveLength(1)
  })

  it('restores detail-only back/forward contexts without list reload, and handles invalid detail with a GET-only retry', async () => {
    let detailReads = 0
    resourceMock.mockImplementation(async name => {
      if (name.startsWith('admin/requests/')) {
        ++detailReads
        return detailReads === 1 ? { error: '请求不存在', status: 404, code: 'not_found', request_id: 'server-diagnostic' } : { data: requestsDetail }
      }
      return { data: [requestsRow] }
    })
    const wrapper = mount(OperationsPane, { props: { view: 'requests', context: { page: '2', detail: 'gone-id' } } })
    await flushPromises()
    const message = wrapper.find('.request-drawer [role="alert"]').text()
    expect(message).toContain('not_found'); expect(message).toContain('server-diagnostic')
    expect(wrapper.find('.pager').text()).toContain('第 2 页')
    await button(wrapper, '重试读取详情').trigger('click'); await flushPromises()
    expect(detailReads).toBe(2); expect(mutations()).toHaveLength(0)
    await wrapper.setProps({ context: { page: '2' } }); await flushPromises()
    expect(wrapper.find('.request-drawer').exists()).toBe(false)
    await wrapper.setProps({ context: { page: '2', detail: 'gone-id' } }); await flushPromises()
    expect(wrapper.find('.request-drawer').exists()).toBe(true)
    expect(listQueries()).toHaveLength(1)
  })

  it('shows loading and actual error metadata, not false empty or invented IDs; retry only reads records', async () => {
    const pending = deferred<{ error: string; code: string; request_id: string; status: number }>()
    resourceMock.mockReturnValueOnce(pending.promise)
    const wrapper = mount(OperationsPane, { props: { view: 'requests' } })
    expect(wrapper.text()).toContain('加载中…'); expect(wrapper.text()).not.toContain('暂无记录')
    pending.resolve({ error: '查询失败', code: 'store_failed', request_id: 'actual-id', status: 500 }); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('store_failed')
    expect(wrapper.find('[role="alert"]').text()).toContain('actual-id')
    expect(wrapper.text()).not.toContain('暂无记录')
    resourceMock.mockResolvedValueOnce({ error: '网络错误' })
    await button(wrapper, '重试读取列表').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).not.toContain('请求 ID')
    expect(mutations()).toHaveLength(0)
  })

  it('rejects invalid UTC ranges/IDs without a request and ignores obsolete list responses after context changes', async () => {
    const pending = deferred<{ data: unknown }>()
    resourceMock.mockReturnValueOnce(pending.promise).mockResolvedValue({ data: [{ ...requestsRow, id: 'latest' }] })
    const wrapper = mount(OperationsPane, { props: { view: 'requests' } })
    await wrapper.setProps({ context: { page: '2', request_id: 'latest' } }); await flushPromises()
    pending.resolve({ data: [{ ...requestsRow, id: 'stale' }] }); await flushPromises()
    expect(wrapper.find('.request-list').text()).toContain('latest'); expect(wrapper.find('.request-list').text()).not.toContain('stale')
    const reads = listQueries().length
    await wrapper.find('[name="from"]').setValue('2026-10-04T00:00:00Z')
    await wrapper.find('[name="to"]').setValue('2026-10-03T00:00:00Z')
    await wrapper.find('form.filters').trigger('submit'); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('不得晚于')
    expect(listQueries()).toHaveLength(reads)
    await wrapper.find('[name="from"]').setValue('')
    await wrapper.find('[name="user_id"]').setValue('0')
    await wrapper.find('form.filters').trigger('submit'); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('正整数')
    expect(listQueries()).toHaveLength(reads)
  })
})

const providerFixture = { id: 2, name: 'test-provider', active: true, type: 'openai_compatible', compatibility_profile: 'generic', base_url: 'https://upstream.example/v1', timeout_ms: 30000, max_concurrency: 0 }
const aliasFixture = { id: 3, name: 'chat-model', active: false }
const routeFixture = { id: 4, provider_id: 2, provider_model: 'upstream-model', active: true, priority: 10 }
const readinessFixture = { ready: true, effective_routes: 1, protocols: [], pricing_status: 'unpriced', reasons: ['未配置价格'], routes: [{ ...routeFixture, provider_name: 'test-provider', provider_active: true, protocols: [], pricing_status: 'unpriced', health_status: 'healthy' }] }
const impactFixture = { resource: 'providers/2', high_risk: true, affected: [{ alias_id: 3, alias_name: 'last-route-model', active: true, remaining_routes: 0, loses_last_route: true }, { alias_id: 5, alias_name: 'fallback-model', active: true, remaining_routes: 2, loses_last_route: false }] }
function configureRouting(ready = readinessFixture, alias = aliasFixture) {
  resourceMock.mockImplementation(async (name, init) => {
    if (init?.method) return { data: { id: 4 } }
    if (name.endsWith('/impact')) return { data: impactFixture }
    if (name.endsWith('/readiness')) return { data: ready }
    if (name.endsWith('/routes')) return { data: [routeFixture] }
    if (name.endsWith('/models')) return { data: [{ id: 1, model_id: 'upstream-model' }] }
    if (name === 'admin/aliases') return { data: [alias] }
    if (name === 'admin/providers') return { data: [providerFixture] }
    return { data: [] }
  })
}

describe('Operations model readiness and classification', () => {
  it('has no removed group UI/endpoints and saves model-group alias association solely as classification', async () => {
    const removed = mount(OperationsPane, { props: { view: 'userGroups' } })
    await flushPromises()
    expect(removed.text()).toContain('页面不可用')
    expect(resourceMock).not.toHaveBeenCalled()
    expect(removed.find('button').exists()).toBe(false)
    resourceMock.mockImplementation(async (name, init) => name === 'admin/aliases' ? { data: [aliasFixture] } : !init?.method ? { data: [] } : { data: { id: 1 } })
    const wrapper = mount(OperationsPane, { props: { view: 'modelGroups' } })
    await flushPromises()
    await button(wrapper, '+ 新建').trigger('click'); await flushPromises()
    await wrapper.find('form.form-panel input[maxlength="200"]').setValue('classification')
    await wrapper.find('fieldset input[type="checkbox"]').setValue(true)
    await wrapper.find('form.form-panel').trigger('submit'); await flushPromises()
    const saved = mutations().find(([name]) => name === 'admin/model-groups')!
    expect(JSON.parse(String(saved[1]!.body))).toEqual({ name: 'classification', active: true, alias_ids: [3] })
    expect(wrapper.text()).toContain('仅用于分类')
    expect(wrapper.text()).not.toContain('授权')
    expect(resourceMock.mock.calls.some(([name]) => name.includes('user-groups'))).toBe(false)
  })

  it('creates inactive alias drafts with enabling requirements, never a default-enabled alias', async () => {
    configureRouting()
    const wrapper = mount(OperationsPane, { props: { view: 'routing' } })
    await flushPromises()
    await button(wrapper, '+ 新建').trigger('click'); await flushPromises()
    expect(wrapper.find('form.form-panel').text()).toContain('未启用草稿')
    expect(wrapper.find('form.form-panel').text()).toContain('至少有一条启用路由')
    await wrapper.find('form.form-panel input[maxlength="200"]').setValue('new-model')
    await wrapper.find('form.form-panel').trigger('submit'); await flushPromises()
    const saved = mutations().find(([name]) => name === 'admin/aliases')!
    expect(JSON.parse(String(saved[1]!.body))).toEqual({ name: 'new-model', active: false })
    expect(wrapper.text()).toContain('草稿已保存')
  })

  it('reads server readiness before enabling, blocks no valid route, and shows actual unknown protocols/pricing links', async () => {
    configureRouting({ ...readinessFixture, ready: false, effective_routes: 0, reasons: ['没有启用供应商的有效路由'] })
    const wrapper = mount(OperationsPane, { props: { view: 'routing' } })
    await flushPromises()
    expect(wrapper.text()).toContain('有效路由 0 条')
    expect(wrapper.text()).toContain('未知（无可确认协议')
    expect(wrapper.text()).toContain('未定价（只提示，不阻止调用）')
    const before = resourceMock.mock.calls.filter(([name]) => name.endsWith('/readiness')).length
    await button(wrapper, '启用', '.op-row button').trigger('click'); await flushPromises()
    expect(resourceMock.mock.calls.filter(([name]) => name.endsWith('/readiness'))).toHaveLength(before + 1)
    expect(wrapper.find('[role="alert"]').text()).toContain('至少需要一条启用路由')
    expect(mutations()).toHaveLength(0)
    await button(wrapper, '价格配置').trigger('click')
    expect(wrapper.emitted('navigate')!.slice(-1)[0]![0]).toEqual({ view: 'prices', context: { model: 'chat-model' } })
  })

  it.each(['free', 'unpriced'])('allows enabling with effective routes for %s pricing; no price blocking', async pricing => {
    configureRouting({ ...readinessFixture, pricing_status: pricing })
    const wrapper = mount(OperationsPane, { props: { view: 'routing' } })
    await flushPromises()
    await button(wrapper, '启用', '.op-row button').trigger('click'); await flushPromises()
    expect(mutations()).toHaveLength(1)
    expect(JSON.parse(String(mutations()[0]![1]!.body))).toEqual({ active: true })
  })

  it('does not disable or block renaming a pre-existing anomalous active alias', async () => {
    configureRouting({ ...readinessFixture, ready: false, effective_routes: 0 }, { ...aliasFixture, active: true })
    const wrapper = mount(OperationsPane, { props: { view: 'routing' } })
    await flushPromises()
    expect(mutations()).toHaveLength(0)
    await button(wrapper, '编辑', '.op-row button').trigger('click'); await flushPromises()
    await wrapper.find('form.form-panel input[maxlength="200"]').setValue('renamed')
    await wrapper.find('form.form-panel').trigger('submit'); await flushPromises()
    expect(JSON.parse(String(mutations()[0]![1]!.body))).toEqual({ name: 'renamed', active: true })
  })

  it('supports associated upstream model selection and explicit manual models without automatically enabling aliases', async () => {
    configureRouting()
    const wrapper = mount(OperationsPane, { props: { view: 'routing', context: { detail: '3' } } })
    await flushPromises()
    expect(wrapper.find('.details').text()).toContain('供应商：启用')
    expect(wrapper.find('[name="route_model"]').text()).toContain('upstream-model')
    await wrapper.find('[name="route_model"]').setValue('upstream-model')
    await wrapper.find('form.route-form').trigger('submit'); await flushPromises()
    expect(JSON.parse(String(mutations()[0]![1]!.body))).toMatchObject({ provider_id: 2, provider_model: 'upstream-model', priority: 20 })
    await wrapper.find('[name="model_mode"]').setValue('manual')
    await wrapper.find('[name="route_model_manual"]').setValue('custom-not-in-directory')
    await wrapper.find('form.route-form').trigger('submit'); await flushPromises()
    expect(JSON.parse(String(mutations()[1]![1]!.body))).toMatchObject({ provider_model: 'custom-not-in-directory' })
    expect(mutations().every(([name]) => name === 'admin/aliases/3/routes')).toBe(true)
    await button(wrapper, '供应商 / 模型目录', '.details button').trigger('click')
    expect(wrapper.emitted('navigate')!.slice(-1)[0]![0]).toEqual({ view: 'providers', context: { detail: '2' } })
    await button(wrapper, '关闭明细', '.details button').trigger('click'); await flushPromises()
    await button(wrapper, '配置路由', '.op-row button').trigger('click'); await flushPromises()
    expect(wrapper.find('[name="route_model"]').text()).toContain('upstream-model')
  })

  it('preserves provider model-directory manual addition and capability editing', async () => {
    configureRouting()
    const wrapper = mount(OperationsPane, { props: { view: 'providers', context: { detail: '2' } } })
    await flushPromises()
    expect(wrapper.find('.model-row').text()).toContain('upstream-model')
    await wrapper.find('.model-row input[type="checkbox"]').setValue(true)
    await button(wrapper, '保存能力').trigger('click'); await flushPromises()
    expect(JSON.parse(String(mutations()[0]![1]!.body))).toMatchObject({ model_id: 'upstream-model', capabilities: { streaming: true } })
    await wrapper.find('.details form input[required]').setValue('manual-model')
    await wrapper.find('.details form').trigger('submit'); await flushPromises()
    expect(JSON.parse(String(mutations()[1]![1]!.body))).toMatchObject({ model_id: 'manual-model' })
  })
})

describe('Operations high-impact confirmation', () => {
  it.each([
    ['providers', '停用', 'admin/providers/2', 'PATCH'],
    ['providers', '删除', 'admin/providers/2', 'DELETE'],
    ['routing', '停用', 'admin/aliases/3', 'PATCH'],
    ['routing', '删除别名', 'admin/aliases/3', 'DELETE'],
  ])('fetches actual impact for %s %s before any mutation; cancel is inert', async (view, label, resource) => {
    configureRouting(readinessFixture, { ...aliasFixture, active: true })
    const pending = deferred<{ data: unknown }>()
    const original = resourceMock.getMockImplementation()!
    resourceMock.mockImplementation((name, init) => name === `${resource}/impact` ? pending.promise : original(name, init))
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mount(OperationsPane, { props: { view } })
    await flushPromises()
    await button(wrapper, label, '.op-row button').trigger('click')
    expect(resourceMock.mock.calls.some(([name]) => name === `${resource}/impact`)).toBe(true)
    expect(confirm).not.toHaveBeenCalled(); expect(mutations()).toHaveLength(0)
    pending.resolve({ data: impactFixture }); await flushPromises()
    expect(confirm).toHaveBeenCalledTimes(1)
    const text = String(confirm.mock.calls[0]![0])
    for (const value of ['2 个别名', '1 个将失去最后有效路由', 'last-route-model', 'fallback-model', '剩余有效路由 0 条', '剩余有效路由 2 条', '仍有替代路由', '不会自动修改']) expect(text).toContain(value)
    expect(mutations()).toHaveLength(0)
  })

  it('requires impact confirmation for route disable/removal and mutates only the target after approval', async () => {
    configureRouting()
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mount(OperationsPane, { props: { view: 'routing', context: { detail: '3' } } })
    await flushPromises()
    await button(wrapper, '停用', '.route-row button').trigger('click'); await flushPromises()
    expect(resourceMock.mock.calls.some(([name]) => name === 'admin/aliases/3/routes/4/impact')).toBe(true)
    expect(mutations()).toHaveLength(0)
    confirm.mockReturnValue(true)
    await button(wrapper, '移除', '.route-row button').trigger('click'); await flushPromises()
    expect(mutations()).toHaveLength(1)
    expect(mutations()[0]![0]).toBe('admin/aliases/3/routes/4')
    expect(mutations()[0]![1]!.method).toBe('DELETE')
  })

  it('does not bypass impact via the provider editor and blocks on impact errors with server metadata', async () => {
    configureRouting()
    const original = resourceMock.getMockImplementation()!
    resourceMock.mockImplementation((name, init) => name.endsWith('/impact') ? Promise.resolve({ error: '影响读取失败', status: 500, code: 'impact_failed', request_id: 'impact-id' }) : original(name, init))
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()
    await button(wrapper, '编辑', '.op-row button').trigger('click'); await flushPromises()
    await wrapper.find('form.form-panel input[type="checkbox"]').setValue(false)
    await wrapper.find('form.form-panel').trigger('submit'); await flushPromises()
    expect(confirm).not.toHaveBeenCalled(); expect(mutations()).toHaveLength(0)
    expect(wrapper.find('[role="alert"]').text()).toContain('impact_failed')
    expect(wrapper.find('[role="alert"]').text()).toContain('impact-id')
  })

  it('does not confirm or mutate after the view/account component unmounts during impact prefetch', async () => {
    configureRouting()
    const pending = deferred<{ data: unknown }>(), original = resourceMock.getMockImplementation()!
    resourceMock.mockImplementation((name, init) => name.endsWith('/impact') ? pending.promise : original(name, init))
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mount(OperationsPane, { props: { view: 'providers' } })
    await flushPromises()
    await button(wrapper, '删除', '.op-row button').trigger('click')
    wrapper.unmount(); pending.resolve({ data: impactFixture }); await flushPromises()
    expect(confirm).not.toHaveBeenCalled(); expect(mutations()).toHaveLength(0)
  })
})

describe('Operations audit filters and safe summaries', () => {
  it('paginates filtered audit arrays and restores UTC/operator/object/action/result context', async () => {
    resourceMock.mockResolvedValue({ data: Array.from({ length: 50 }, (_, id) => ({ ...auditRows[0], id, result: 'failure' })) })
    const context = { page: '2', from: '2026-10-03T00:00:00Z', to: '2026-10-03T23:59:59Z', actor_id: '2', resource: 'providers', action: 'PATCH', result: 'failure' }
    const wrapper = mount(OperationsPane, { props: { view: 'audit', context } })
    await flushPromises()
    expect(listQueries('audit')[0]!.get('offset')).toBe('50')
    for (const key of ['actor_id', 'resource', 'action', 'result']) expect(listQueries('audit')[0]!.get(key)).toBe((context as Record<string, string>)[key])
    await button(wrapper, '下一页').trigger('click'); await flushPromises()
    expect(wrapper.emitted('context-change')!.slice(-1)[0]![0]).toMatchObject({ page: '3', actor_id: '2', result: 'failure' })
    expect(listQueries('audit').slice(-1)[0]!.get('offset')).toBe('100')
    expect(mutations()).toHaveLength(0)
  })

  it('shows real failure/denied outcomes and whitelisted before/after; hides secrets, hashes and legacy detail', async () => {
    const unsafe = { name: 'safe-name', active: true, api_key: 'fixture-secret', password: 'fixture-password', hash: 'fixture-hash', client_secret: 'fixture-oidc', body: 'fixture-content', base_url: 'https://fixture-credential@example.invalid', arbitrary: { token: 'fixture-nested' } }
    resourceMock.mockResolvedValue({ data: [{ ...auditRows[0], result: 'failure', before: unsafe, after: JSON.stringify({ ...unsafe, active: false }) }, { ...auditRows[1], result: 'denied', detail: 'fixture-content' }, { ...auditRows[0], id: 12 }] })
    const wrapper = mount(OperationsPane, { props: { view: 'audit' } })
    await flushPromises()
    const rows = wrapper.findAll('.audit-row')
    expect(rows[0]!.text()).toContain('实际结果：失败'); expect(rows[1]!.text()).toContain('实际结果：拒绝')
    expect(rows[2]!.text()).toContain('未知（历史记录未记录结果）')
    expect(rows[0]!.text()).toContain('safe-name'); expect(rows[0]!.text()).toContain('false')
    for (const sensitive of ['fixture-secret', 'fixture-password', 'fixture-hash', 'fixture-oidc', 'fixture-content', 'fixture-credential', 'fixture-nested', 'admin API mutation']) expect(wrapper.text()).not.toContain(sensitive)
    expect(rows[0]!.findAll('button')).toHaveLength(0)
  })
})

describe('Operations readiness read regressions', () => {
  it('localizes backend readiness reason codes and shows provider status instead of guessing capabilities', async () => {
    configureRouting({ ...readinessFixture, ready: false, effective_routes: 0, reasons: ['provider_inactive', 'pricing_missing', 'capabilities_unknown', 'no_effective_route'], routes: [{ ...readinessFixture.routes[0]!, provider_active: false }] })
    const wrapper = mount(OperationsPane, { props: { view: 'routing' } })
    await flushPromises()
    expect(wrapper.text()).toContain('test-provider（停用）')
    for (const value of ['供应商已停用', '缺少价格版本', '模型能力未知', '没有指向启用供应商']) expect(wrapper.text()).toContain(value)
    expect(wrapper.text()).not.toContain('capabilities_unknown')
    expect(wrapper.text()).not.toContain('provider_inactive')
  })

  it('does not bypass the effective-route gate by enabling from the alias editor', async () => {
    configureRouting({ ...readinessFixture, effective_routes: 0, ready: false })
    const wrapper = mount(OperationsPane, { props: { view: 'routing' } })
    await flushPromises()
    await button(wrapper, '编辑', '.op-row button').trigger('click'); await flushPromises()
    await wrapper.find('form.form-panel input[type="checkbox"]').setValue(true)
    await wrapper.find('form.form-panel').trigger('submit'); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('至少需要一条启用路由')
    expect(mutations()).toHaveLength(0)
  })

  it('ignores stale provider model-directory reads when the associated provider changes', async () => {
    configureRouting()
    const oldModels = deferred<{ data: unknown }>(), original = resourceMock.getMockImplementation()!
    resourceMock.mockImplementation((name, init) => {
      if (name === 'admin/providers') return Promise.resolve({ data: [providerFixture, { ...providerFixture, id: 9, name: 'second-provider' }] })
      if (name === 'admin/providers/2/models') return oldModels.promise
      if (name === 'admin/providers/9/models') return Promise.resolve({ data: [{ model_id: 'new-provider-model' }] })
      return original(name, init)
    })
    const wrapper = mount(OperationsPane, { props: { view: 'routing', context: { detail: '3' } } })
    await flushPromises()
    const signal = resourceMock.mock.calls.find(([name]) => name === 'admin/providers/2/models')![1]!.signal as AbortSignal
    expect(wrapper.find('[name="route_model"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[name="route_provider"]').setValue('9'); await flushPromises()
    expect(signal.aborted).toBe(true)
    oldModels.resolve({ data: [{ model_id: 'old-provider-model' }] }); await flushPromises()
    expect(wrapper.find('[name="route_model"]').text()).toContain('new-provider-model')
    expect(wrapper.find('[name="route_model"]').text()).not.toContain('old-provider-model')
    await wrapper.find('[name="route_model"]').setValue('new-provider-model')
    await wrapper.find('form.route-form').trigger('submit'); await flushPromises()
    expect(JSON.parse(String(mutations()[0]![1]!.body))).toMatchObject({ provider_id: 9, provider_model: 'new-provider-model' })
  })
})

describe('Operations precise UTC filter inputs', () => {
  it('submits edited numeric filters and keeps submillisecond UTC boundaries exact', async () => {
    resourceMock.mockResolvedValue({ data: [] })
    const wrapper = mount(OperationsPane, { props: { view: 'requests' } })
    await flushPromises()
    await wrapper.find('[name="user_id"]').setValue('12')
    await wrapper.find('[name="api_key_id"]').setValue('34')
    await wrapper.find('[name="provider_id"]').setValue('56')
    await wrapper.find('[name="model_group_id"]').setValue('78')
    await wrapper.find('[name="from"]').setValue('2026-10-03T08:00:00.123456+08:00')
    await wrapper.find('[name="to"]').setValue('2026-10-03T00:00:00.123456789Z')
    await wrapper.find('form.filters').trigger('submit'); await flushPromises()
    const query = listQueries().slice(-1)[0]!
    expect(query.get('user_id')).toBe('12'); expect(query.get('api_key_id')).toBe('34')
    expect(query.get('provider_id')).toBe('56'); expect(query.get('model_group_id')).toBe('78')
    expect(query.get('from')).toBe('2026-10-03T00:00:00.123456Z')
    expect(query.get('to')).toBe('2026-10-03T00:00:00.123456789Z')
    const reads = listQueries().length
    await wrapper.find('[name="from"]').setValue('2026-02-31T00:00:00Z')
    await wrapper.find('form.filters').trigger('submit'); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('UTC 日期时间无效')
    expect(listQueries()).toHaveLength(reads)
  })
})
