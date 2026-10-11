import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PriceList from './PriceList.vue'

const wrappers: ReturnType<typeof mount>[] = []
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.unstubAllGlobals() })

describe('read-only immutable price list', () => {
  it('displays both independent units and exact Token/custom strings without controls or API calls', () => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    const prices = [{ model_id: 'upstream', version: 8,
      cost: { input: '0.000001234567890123456789', image: '1.23000e-1000' },
      credits: { input: '0.0002', image: '0', custom: '9007199254740993.0001' },
    }]
    const before = JSON.stringify(prices)
    const wrapper = mount(PriceList, { props: { prices } })
    wrappers.push(wrapper)
    const token = wrapper.findAll('tr[data-meter]').find(row => row.attributes('data-meter') === 'input')!
    expect(token.text()).toContain('每百万 Token')
    expect(token.text()).toContain('¥1.234567890123456789 CNY')
    expect(token.text()).toContain('200 积分')
    const image = wrapper.findAll('tr[data-meter]').find(row => row.attributes('data-meter') === 'image')!
    expect(image.text()).toContain('每 1 实际计量单位')
    expect(image.text()).toContain('1.23000e-1000')
    const custom = wrapper.findAll('tr[data-meter]').find(row => row.attributes('data-meter') === 'custom')!
    expect(custom.text()).toContain('未配置（不是免费）')
    expect(custom.text()).toContain('9007199254740993.0001')
    expect(wrapper.text()).toContain('v8')
    expect(wrapper.findAll('button')).toHaveLength(0)
    expect(fetch).not.toHaveBeenCalled()
    expect(JSON.stringify(prices)).toBe(before)
  })

  it('labels only explicit complete rate configurations, not zero historical amounts or tiny rates', () => {
    const wrapper = mount(PriceList, { props: { prices: [
      { model_id: 'free', version: 1, cost: { input: '-0e-1000' }, credits: { input: '+0' } },
      { model_id: 'waiver', version: 2, cost: { input: '1e-1000' }, credits: { input: '0' } },
      { model_id: 'tiny', version: 1, cost: { input: '1e-1000' }, credits: { input: '1e-1000' } },
      { model_id: 'missing', version: 1, cost: { input: '0' }, credits: {} },
      { model_id: 'history', version: 1, cost: '0', credits: '0', quantity: 10 },
    ] } })
    wrappers.push(wrapper)
    expect(wrapper.findAll('[data-testid="price-classification"]').map(tag => tag.text())).toEqual([
      '免费', '免积分（仍计成本）', '已定价', '配置无效', '配置无效',
    ])
    expect(wrapper.findAll('[role="status"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('配置无效，不可据此推断为免费')
  })

  it('shows an explicit empty state when no price versions exist', () => {
    const wrapper = mount(PriceList, { props: { prices: [] } })
    wrappers.push(wrapper)
    expect(wrapper.text()).toContain('暂无价格版本')
    expect(wrapper.text()).toContain('未定价不等于免费')
  })
})
