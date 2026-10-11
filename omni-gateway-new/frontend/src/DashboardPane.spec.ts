import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import DashboardPane from './DashboardPane.vue'

function fakeResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  }
}

beforeEach(() => {
  sessionStorage.clear()
  const stats = {
    requests: 42,
    success_rate: 99.5,
    cost: 3,
    credits: 5,
    total_tokens: 1234,
    p50_latency_ms: 100,
    p95_latency_ms: 200,
    p99_latency_ms: 300,
    p95_ttft_ms: 50,
    failover_count: 1,
    settlement_errors: 0,
    trend: [],
    routes: [
      {
        model_alias: 'gpt-x',
        provider_id: 9,
        provider_model: 'gpt-x-upstream',
        attempts: 10,
        success_rate: 90,
        p95_latency_ms: 250,
        cost: 1.5,
        credits: 3,
        settlement_errors: 0,
      },
    ],
  }
  const fetchMock = vi.fn((url: string) => {
    if (url.startsWith('/api/admin/stats')) return Promise.resolve(fakeResponse(stats))
    if (url.startsWith('/api/admin/runtime')) return Promise.resolve(fakeResponse({ records: { pending: 2 }, billing: { pending: 1 } }))
    if (url.startsWith('/api/admin/providers'))
      return Promise.resolve(fakeResponse([{ id: 9, name: 'Provider Nine', active: true }]))
    if (url.startsWith('/readyz')) return Promise.resolve(fakeResponse('ok'))
    return Promise.resolve(fakeResponse({}, 404))
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('DashboardPane', () => {
  it('renders stat cards and route table from the stubbed API responses', async () => {
    const wrapper = mount(DashboardPane)
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('请求数')
    expect(text).toContain('42')
    expect(text).toContain('成功率')
    expect(text).toContain('99.5%')
    expect(text).toContain('1234')
    expect(text).toContain('gpt-x-upstream')
    expect(text).toContain('Provider Nine')
    // runtime pending = records.pending + billing.pending
    expect(text).toContain('待持久化任务 3')
    // /readyz resolved ok -> database ready normal
    expect(text).toContain('数据库就绪 正常')
    // two-ledger model: CNY cost and credits are shown as separate cards
    expect(text).toContain('上游成本')
    expect(text).toContain('积分消耗')
    // profit (毛利) was removed from the API and must never render
    expect(text).not.toContain('毛利')
    // no error alert rendered
    expect(wrapper.find('.alert').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows an error alert when the stats endpoint fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url.startsWith('/api/admin/stats'))
          return Promise.resolve(fakeResponse({ error: { message: 'forbidden' } }, 403))
        if (url.startsWith('/api/admin/runtime')) return Promise.resolve(fakeResponse({}))
        if (url.startsWith('/api/admin/providers')) return Promise.resolve(fakeResponse([]))
        return Promise.resolve(fakeResponse('ok'))
      }),
    )

    const wrapper = mount(DashboardPane)
    await flushPromises()

    const alert = wrapper.find('.alert')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('没有权限执行此操作')
    expect(alert.find('button').text()).toBe('重试读取')

    wrapper.unmount()
  })
  it('免费调用统计仍展示正请求与 Token，以及零成本和零积分', async () => {
    const stats = {
      requests: 7, success_rate: 100, cost: 0, credits: 0, total_tokens: 789,
      p50_latency_ms: 10, p95_latency_ms: 20, p99_latency_ms: 30, p95_ttft_ms: 5,
      failover_count: 0, settlement_errors: 0, trend: [],
      routes: [{ model_alias: 'free-model', provider_id: 9, provider_model: 'free-upstream', attempts: 7, success_rate: 100, p95_latency_ms: 20, cost: 0, credits: 0, settlement_errors: 0 }],
    }
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.startsWith('/api/admin/stats')) return Promise.resolve(fakeResponse(stats))
      if (url.startsWith('/api/admin/runtime')) return Promise.resolve(fakeResponse({}))
      if (url.startsWith('/api/admin/providers')) return Promise.resolve(fakeResponse([{ id: 9, name: 'Free Provider', active: true }]))
      if (url.startsWith('/readyz')) return Promise.resolve(fakeResponse('ok'))
      return Promise.resolve(fakeResponse({}, 404))
    }))

    const wrapper = mount(DashboardPane)
    await flushPromises()
    const cardText = (label: string) => wrapper.findAll('.cards .card').find(card => card.find('.card-label').text() === label)!.text()

    expect(cardText('请求数')).toContain('7')
    expect(cardText('Token')).toContain('789')
    expect(cardText('上游成本')).toContain('¥0')
    expect(cardText('积分消耗')).toContain('0')
    expect(wrapper.text()).toContain('free-upstream')
    expect(wrapper.text()).not.toContain('免费')
    wrapper.unmount()
  })
  it('从 URL 上下文恢复统计筛选，修改筛选发出稳定上下文', async () => {
    const wrapper = mount(DashboardPane, {props:{context:{period:'168',api:'responses',user_id:'2',provider_id:'9',model_group_id:'4'}}})
    await flushPromises()
    const urls = (fetch as any).mock.calls.map((call:any[]) => call[0] as string)
    const query = new URLSearchParams(urls.find((url:string) => url.startsWith('/api/admin/stats'))!.split('?')[1])
    expect(query.get('api')).toBe('responses')
    expect(query.get('user_id')).toBe('2')
    expect(query.get('provider_id')).toBe('9')
    expect(query.get('model_group_id')).toBe('4')
    await wrapper.find('select').setValue('720')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.emitted('context-change')?.[0]?.[0]).toEqual({period:'720',api:'responses',user_id:'2',provider_id:'9',model_group_id:'4'})
    await wrapper.setProps({context:{period:'24',api:'chat_completions'}})
    await flushPromises()
    expect((wrapper.findAll('select')[1].element as HTMLSelectElement).value).toBe('chat_completions')
    wrapper.unmount()
  })

  it('统计仍在加载时不把请求数或路由列表误报为零或空', async () => {
    let complete!: (value:unknown) => void
    const pending = new Promise(resolve => {complete = resolve})
    vi.stubGlobal('fetch',vi.fn((url:string) => url.startsWith('/api/admin/stats') ? pending : Promise.resolve(fakeResponse([]))))
    const wrapper = mount(DashboardPane)
    await flushPromises()
    expect(wrapper.find('[role="status"]').text()).toContain('正在加载统计')
    expect(wrapper.find('.cards').exists()).toBe(false)
    expect(wrapper.find('.empty').exists()).toBe(false)
    complete(fakeResponse({requests:1,trend:[]}))
    await flushPromises()
    expect(wrapper.find('.cards').exists()).toBe(true)
    wrapper.unmount()
  })
})
