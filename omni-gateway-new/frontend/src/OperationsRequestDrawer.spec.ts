/// <reference types="vite/client" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import drawerSource from './OperationsRequestDrawer.vue?raw'
import paneSource from './OperationsPane.vue?raw'

const { resourceMock } = vi.hoisted(() => ({ resourceMock: vi.fn<(name: string, init?: RequestInit) => Promise<any>>() }))
vi.mock('./api', () => ({ api: { resource: resourceMock } }))
import OperationsRequestDrawer from './OperationsRequestDrawer.vue'
import OperationsPane from './OperationsPane.vue'

enableAutoUnmount(afterEach)
afterEach(() => vi.restoreAllMocks())
const detailFor = (id: string, settlement_status = 'free') => ({
  request: { id, user_id: 2, api_key_id: 3, created_at: '2026-10-03T08:00:00+08:00', status: 'success', settlement_status, model_alias: 'model', api: 'chat_completions', cost: '0', credits: '0', total_latency_ms: 42, ttft_ms: 12, usage: { meters: { input_tokens: 12, custom: 3 }, estimated: true } },
  attempts: [{ settlement_id: `settlement-${id}`, provider_model: 'upstream', status: 'success', status_code: 200, latency_ms: 42, cost: '0', credits: '0' }],
  ledger: [{ id: 1, settlement_id: `settlement-${id}`, meter: 'custom', quantity: '3', estimated: true, cost: '0', credits: '0', price_version: 9 }],
})
function deferred<T = any>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
function button(wrapper: any, label: string) { return wrapper.findAll('button').find((item: any) => item.text() === label)! }
const contentCalls = () => resourceMock.mock.calls.filter(([name]) => name.startsWith('admin/content/'))
beforeEach(() => {
  resourceMock.mockReset().mockImplementation(async name => name.startsWith('admin/requests/') ? { data: detailFor(name.split('/').pop()!) } : { data: {} })
})

describe('OperationsRequestDrawer read boundaries', () => {
  it('shows a skeleton immediately, keeps actual attempts/usage/zero ledger quantities, and does not read content by default', async () => {
    const pending = deferred()
    resourceMock.mockReturnValue(pending.promise)
    const wrapper = mount(OperationsRequestDrawer, { props: { requestId: 'gateway-id' } })
    expect(wrapper.find('[aria-busy="true"]').text()).toContain('正在加载请求详情')
    expect(wrapper.find('[role="dialog"]').attributes('aria-modal')).toBe('true')
    expect(wrapper.find('.request-detail').exists()).toBe(false)
    pending.resolve({ data: detailFor('gateway-id') }); await flushPromises()
    const facts = wrapper.find('.request-facts').text()
    expect(facts).toContain('免费（显式零费率）'); expect(facts).toContain('2026-10-03 00:00:00.000 UTC')
    expect(wrapper.find('.attempt-row').text()).toContain('HTTP 200')
    const ledger = wrapper.find('.scroll tbody tr').text()
    for (const value of ['custom（估算）', '3', '0', '9']) expect(ledger).toContain(value)
    expect(wrapper.find('pre').text()).toContain('input_tokens')
    expect(contentCalls()).toHaveLength(0)
    expect(button(wrapper, '查看内容日志').attributes('disabled')).toBeDefined()
    expect(resourceMock.mock.calls.every(([, init]) => !init?.method)).toBe(true)
  })

  it('preserves real code/request_id on errors and retries detail only, without model invocation or fabricated metadata', async () => {
    resourceMock.mockResolvedValueOnce({ error: '详情不存在', status: 404, code: 'not_found', request_id: 'diagnostic-id' })
    const wrapper = mount(OperationsRequestDrawer, { props: { requestId: 'gone' } })
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('not_found')
    expect(wrapper.find('[role="alert"]').text()).toContain('diagnostic-id')
    resourceMock.mockResolvedValueOnce({ error: '网络错误' })
    await button(wrapper, '重试读取详情').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).not.toContain('诊断请求 ID')
    expect(resourceMock.mock.calls.map(([name]) => name)).toEqual(['admin/requests/gone', 'admin/requests/gone'])
    expect(contentCalls()).toHaveLength(0)
  })

  it('ignores stale detail responses on changed deep links and clears detail immediately before the next read', async () => {
    const old = deferred(), latest = deferred()
    resourceMock.mockImplementation(name => name.endsWith('/old') ? old.promise : latest.promise)
    const wrapper = mount(OperationsRequestDrawer, { props: { requestId: 'old' } })
    const oldSignal = resourceMock.mock.calls[0]![1]!.signal as AbortSignal
    await wrapper.setProps({ requestId: 'latest' })
    expect(oldSignal.aborted).toBe(true)
    expect(wrapper.find('.detail-skeleton').exists()).toBe(true)
    latest.resolve({ data: detailFor('latest') }); await flushPromises()
    old.resolve({ data: detailFor('obsolete-sensitive-result') }); await flushPromises()
    expect(wrapper.find('.request-detail').text()).toContain('latest')
    expect(wrapper.text()).not.toContain('obsolete-sensitive-result')
  })

  it('requires explicit consent and performs only a single audited content read per opening, including rapid clicks', async () => {
    const pending = deferred()
    resourceMock.mockImplementation(async name => name.startsWith('admin/content/') ? pending.promise : { data: detailFor('one') })
    const wrapper = mount(OperationsRequestDrawer, { props: { requestId: 'one' } })
    await flushPromises()
    const read = button(wrapper, '查看内容日志')
    await read.trigger('click'); expect(contentCalls()).toHaveLength(0)
    await wrapper.find('input[type="checkbox"]').setValue(true)
    await read.trigger('click'); await read.trigger('click')
    expect(contentCalls()).toHaveLength(1)
    expect(wrapper.text()).toContain('正在读取内容日志')
    pending.resolve({ data: { prompt: 'dedicated-local-fixture', response: 'local-answer' } }); await flushPromises()
    expect(wrapper.find('.content-log').text()).toContain('dedicated-local-fixture')
    expect(button(wrapper, '此次内容日志已读取').attributes('disabled')).toBeDefined()
    await button(wrapper, '此次内容日志已读取').trigger('click')
    expect(contentCalls()).toHaveLength(1)
    await button(wrapper, '关闭明细').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    const state = wrapper.vm as unknown as { content: any; detail: any; consent: boolean }
    expect(state.content).toBeNull(); expect(state.detail).toBeNull(); expect(state.consent).toBe(false)
    expect(wrapper.text()).not.toContain('dedicated-local-fixture')
  })

  it('does not silently retry an audited content read after failure', async () => {
    resourceMock.mockImplementation(async name => name.startsWith('admin/content/') ? { error: '内容日志未启用', code: 'content_disabled', request_id: 'content-read-id' } : { data: detailFor('one') })
    const wrapper = mount(OperationsRequestDrawer, { props: { requestId: 'one' } })
    await flushPromises()
    await wrapper.find('input[type="checkbox"]').setValue(true)
    await button(wrapper, '查看内容日志').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('content_disabled')
    expect(wrapper.find('[role="alert"]').text()).toContain('content-read-id')
    expect(contentCalls()).toHaveLength(1)
    expect(button(wrapper, '此次内容日志已读取').attributes('disabled')).toBeDefined()
  })

  it('ignores stale content on request changes and starts the next detail with no consent or body', async () => {
    const oldContent = deferred()
    resourceMock.mockImplementation(async name => name.startsWith('admin/content/') ? oldContent.promise : { data: detailFor(name.split('/').pop()!) })
    const wrapper = mount(OperationsRequestDrawer, { props: { requestId: 'first' } })
    await flushPromises()
    await wrapper.find('input[type="checkbox"]').setValue(true)
    await button(wrapper, '查看内容日志').trigger('click')
    const signal = contentCalls()[0]![1]!.signal as AbortSignal
    await wrapper.setProps({ requestId: 'second' }); await flushPromises()
    expect(signal.aborted).toBe(true)
    expect((wrapper.find('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)
    oldContent.resolve({ data: { prompt: 'stale-private-body', response: 'stale-answer' } }); await flushPromises()
    expect(wrapper.find('.content-log').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('stale-private-body')
    expect(button(wrapper, '查看内容日志').attributes('disabled')).toBeDefined()
  })

  it('clears loaded sensitive state and aborts/ignores pending reads on account unmount', async () => {
    const pending = deferred()
    resourceMock.mockImplementation(async name => name.startsWith('admin/content/') ? pending.promise : { data: detailFor('account') })
    const wrapper = mount(OperationsRequestDrawer, { props: { requestId: 'account' } })
    await flushPromises()
    await wrapper.find('input[type="checkbox"]').setValue(true)
    await button(wrapper, '查看内容日志').trigger('click')
    const state = wrapper.vm as unknown as { content: any; detail: any; consent: boolean }
    const signal = contentCalls()[0]![1]!.signal as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    pending.resolve({ data: { prompt: 'late-account-body' } }); await flushPromises()
    expect(state.content).toBeNull(); expect(state.detail).toBeNull(); expect(state.consent).toBe(false)
    const loaded = mount(OperationsRequestDrawer, { props: { requestId: 'account' } })
    await flushPromises()
    resourceMock.mockImplementation(async name => name.startsWith('admin/content/') ? { data: { prompt: 'loaded-account-body' } } : { data: detailFor('account') })
    await loaded.find('input[type="checkbox"]').setValue(true)
    await button(loaded, '查看内容日志').trigger('click'); await flushPromises()
    const loadedState = loaded.vm as unknown as { content: any; detail: any }
    expect(loadedState.content).not.toBeNull()
    loaded.unmount()
    expect(loadedState.content).toBeNull(); expect(loadedState.detail).toBeNull()
  })

  it('ignores a late detail after closing or switching Operations views', async () => {
    const pending = deferred()
    resourceMock.mockImplementation(async name => name.startsWith('admin/requests/') ? pending.promise : { data: [] })
    const wrapper = mount(OperationsPane, { props: { view: 'requests', context: { detail: 'late' } } })
    await nextTick()
    const signal = resourceMock.mock.calls.find(([name]) => name === 'admin/requests/late')![1]!.signal as AbortSignal
    await wrapper.setProps({ view: 'providers', context: {} }); await flushPromises()
    expect(signal.aborted).toBe(true)
    pending.resolve({ data: detailFor('obsolete-private-detail') }); await flushPromises()
    expect(wrapper.find('.request-drawer').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('obsolete-private-detail')
    expect(contentCalls()).toHaveLength(0)
  })
})

describe('OperationsRequestDrawer keyboard and layout contract', () => {
  it('traps Tab/Shift+Tab and programmatic focus, Escape closes, and unmount restores the original row without scrolling', async () => {
    const original = document.createElement('button'); document.body.appendChild(original); original.focus()
    const focus = vi.spyOn(original, 'focus')
    const overflow = document.body.style.overflow
    const wrapper = mount(OperationsRequestDrawer, { attachTo: document.body, props: { requestId: 'keyboard' } })
    await flushPromises()
    const close = wrapper.find('[aria-label="关闭请求详情"]').element as HTMLButtonElement
    expect(document.activeElement).toBe(close); expect(document.body.style.overflow).toBe('hidden')
    const controls = wrapper.findAll('button:not([disabled]), input:not([disabled])')
    const last = controls[controls.length - 1]!.element as HTMLElement
    close.focus(); document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(last)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(close)
    original.focus(); expect(document.activeElement).toBe(close)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await nextTick()
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
    expect(document.activeElement).toBe(original)
    expect(focus).toHaveBeenLastCalledWith({ preventScroll: true })
    expect(document.body.style.overflow).toBe(overflow)
    original.remove()
  })

  it('uses a fixed desktop right drawer, independent vertical scrolling and full-width mobile panel; compact table becomes tight cards', () => {
    // jsdom cannot measure responsive layout; keep the CSS boundary covered and leave real viewport acceptance to the main agent.
    const drawer = drawerSource
    const pane = paneSource
    expect(drawer).toMatch(/\.request-overlay\{[^}]*position:fixed;[^}]*justify-content:flex-end/)
    expect(drawer).toMatch(/\.drawer-body\{[^}]*min-height:0;overflow-y:auto/)
    expect(drawer).toMatch(/@media\(max-width:720px\)\{\.request-drawer\{width:100%;min-width:0/)
    expect(pane).toMatch(/@media\(max-width:720px\)/)
    expect(pane).toMatch(/\.request-table tr\{display:grid;grid-template-columns:1fr 1fr;gap:7px 12px;padding:12px/)
  })
})
