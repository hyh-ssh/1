import { describe, expect, it } from 'vitest'
import {
  decimalSign, displayToRate, freePriceMaps, meterIsToken, parseRateMap,
  priceClassification, priceMapsToRows, priceRowsToMaps, quotePrice,
  rateToDisplay, resolvePrice, scaleDecimal, suggestNextVersion, validatePriceMaps,
} from './priceMath'

const defaults = {
  cost: { input: '0.000001', output: '0.000002' },
  credits: { input: '0.0002', output: '0.0004' },
}

describe('exact decimal rate units', () => {
  it.each([
    ['0.000001', '1'],
    ['1.234567890123456789e-6', '1.234567890123456789'],
    ['900719925474099312345.123456789', '900719925474099312345123456.789'],
    ['.000000000000000000000001', '0.000000000000000001'],
    ['+2.50E+3', '2500000000'],
    ['-0e-1000', '0'],
  ])('scales %s per token to %s per million without floating point', (unit, million) => {
    expect(rateToDisplay('input', unit)).toBe(million)
    expect(decimalSign(displayToRate('input', million))).toBe(decimalSign(unit))
    expect(scaleDecimal(displayToRate('input', million), 6)).toBe(million)
  })

  it('preserves arbitrary tiny and huge exponents without underflow or giant allocation', () => {
    expect(scaleDecimal('1e-100000000000', 6)).toBe('1e-99999999994')
    expect(scaleDecimal('1e100000000000', -6)).toBe('1e99999999994')
    expect(decimalSign('1e-1000')).toBe(1)
    expect(decimalSign('-1e-1000')).toBe(-1)
    expect(displayToRate('input', rateToDisplay('input', '1e-1000'))).toBe(`0.${'0'.repeat(999)}1`)
  })

  it('only converts confirmed Token keys, never arbitrary custom meters', () => {
    for (const meter of ['input', 'output', 'cache_input', 'cache_creation', 'reasoning', 'total']) {
      expect(meterIsToken(meter)).toBe(true)
      expect(rateToDisplay(meter, '0.000001')).toBe('1')
    }
    for (const meter of ['image', 'audio', 'requests', 'cache_read', 'vendor.custom/token']) {
      expect(meterIsToken(meter)).toBe(false)
      expect(rateToDisplay(meter, '1.234500e-1000')).toBe('1.234500e-1000')
      expect(displayToRate(meter, '1.234500e-1000')).toBe('1.234500e-1000')
    }
  })
})

describe('price maps and lossless table roundtrip', () => {
  it('keeps original strings, credits-only custom keys, and tiny Token precision on roundtrip', () => {
    const maps = {
      cost: { input: '1.2300e-1000', image: '12345678901234567890.1234567890123456789' },
      credits: { input: '+0.0000000000000000000001', image: '0', 'custom/meter': '0.0000010000' },
    }
    const rows = priceMapsToRows(maps)
    expect(priceRowsToMaps(rows)).toEqual(maps)
    rows[0]!.cost = '2.34567890123456789'
    expect(priceRowsToMaps(rows).cost.input).toBe('0.00000234567890123456789')
    expect(priceRowsToMaps(rows).credits['custom/meter']).toBe('0.0000010000')
  })

  it.each([
    [undefined, '必须'], [null, '必须'], ['[]', '对象'], ['{broken', 'JSON 格式无效'],
    ['{}', '至少'], ['{"input":0.5}', '十进制字符串'], ['{"input":1e-1000}', '十进制字符串'],
    ['{"input":false}', '十进制字符串'], ['{"input":"NaN"}', '非负'],
    ['{"input":"-1e-1000"}', '非负'], ['{"input":"1e"}', '非负'],
    ['{"input":"0x10"}', '非负'], ['{"input":" Infinity"}', '非负'],
  ])('rejects invalid map %s', (value, message) => {
    expect(() => parseRateMap(value, '成本')).toThrow(String(message))
  })

  it('requires both nonempty maps and credits covering every cost meter; zero is legitimate', () => {
    expect(() => validatePriceMaps({ input: '1' }, { output: '0' })).toThrow('input')
    expect(() => validatePriceMaps({ input: '0' }, undefined)).toThrow('积分')
    expect(validatePriceMaps({ input: '-0' }, { input: '+0e-1000' }).cost.input).toBe('-0')
    expect(() => priceRowsToMaps([{ meter: 'input', cost: '1', credits: '' }])).toThrow('input')
    expect(() => priceRowsToMaps([
      { meter: 'image', cost: '1', credits: '2' }, { meter: 'image', cost: '0', credits: '0' },
    ])).toThrow('重复')
  })

  it('preserves prototype-looking custom meter names as own keys', () => {
    const cost = '{"__proto__":"1e-1000","constructor":"0"}'
    const credits = '{"__proto__":"0","constructor":"0"}'
    expect(JSON.parse(JSON.stringify(priceRowsToMaps(priceMapsToRows(validatePriceMaps(cost, credits)))))).toEqual({
      cost: JSON.parse(cost), credits: JSON.parse(credits),
    })
  })
})

describe('explicit free configurations, never historical amounts', () => {
  it('zeros the input/output/custom union without mutating either input', () => {
    const cost = { input: '0.01', image: '2' }
    const credits = { output: '1e-1000', 'custom/meter': '3' }
    const result = freePriceMaps(cost, credits)
    const union = { input: '0', output: '0', image: '0', 'custom/meter': '0' }
    expect(result).toEqual({ cost: union, credits: union })
    expect(result.cost).not.toBe(result.credits)
    expect(cost.image).toBe('2')
    expect(credits.output).toBe('1e-1000')
    expect(priceClassification(result)).toBe('free')
  })

  it('waives credits independently and retains all exact cost strings', () => {
    const cost = { input: '1e-1000', image: '2.00000' }
    const result = freePriceMaps(cost, { custom: '3' }, true)
    expect(result.cost).toEqual(cost)
    expect(result.credits).toEqual({ input: '0', output: '0', image: '0', custom: '0' })
    expect(priceClassification(result)).toBe('credits_free')
    expect(() => freePriceMaps(cost, '{broken')).toThrow('积分')
  })

  it('classifies only complete, valid explicit rate configurations', () => {
    expect(priceClassification({ cost: '0', credits: '0', quantity: 10 })).toBe('invalid')
    expect(priceClassification({ amount: '0', cost_amount: '0' })).toBe('unpriced')
    expect(priceClassification({ cost: { input: '0' }, credits: {} })).toBe('invalid')
    expect(priceClassification({ cost: { input: '1e-1000' }, credits: { input: '1e-1000' } })).toBe('priced')
    expect(priceClassification({ cost: { input: '0e-1000' }, credits: { input: '-0.000' } })).toBe('free')
    expect(priceClassification(defaults)).toBe('priced')
  })
})

describe('independent local quote and actual price matching', () => {
  it('computes the house example with separate ledgers and actual Token quantities', () => {
    const result = quotePrice(defaults, { input: '1000', output: '2000' })
    expect(result.costExact).toBe('0.005')
    expect(result.costDisplay).toBe('0.005000')
    expect(result.creditsExact).toBe('1')
    expect(result.credits).toBe('1.000')
  })

  it.each([
    ['0', '0', '0', '0.000', 'free'],
    ['0.1', '0', '0.2', '0.000', 'credits_free'],
    ['0', '0.25', '0', '0.500', 'priced'],
    ['0.1', '0.25', '0.2', '0.500', 'priced'],
  ])('quotes independent configurations cost=%s credits=%s', (cost, credits, costExpected, creditExpected, classification) => {
    const maps = { cost: { custom: cost }, credits: { custom: credits } }
    expect(quotePrice(maps, { custom: '2' })).toMatchObject({ costExact: costExpected, credits: creditExpected })
    expect(priceClassification(maps)).toBe(classification)
  })

  it('rounds only readable CNY display and keeps its unquantized exact value', () => {
    expect(quotePrice({ cost: { image: '0.0000005' }, credits: { image: '0' } }, { image: '1' }))
      .toMatchObject({ costExact: '0.0000005', costDisplay: '0.000001' })
  })

  it('rounds credits per meter (half away from zero), not the final raw sum; cost stays exact', () => {
    const result = quotePrice({
      cost: { image: '0.000000123456789', custom: '0.25' },
      credits: { image: '0.0005', custom: '0.0005' },
    }, { image: '1', custom: '1' })
    expect(result.costExact).toBe('0.250000123456789')
    expect(result.costDisplay).toBe('0.250000')
    expect(result.creditsExact).toBe('0.001')
    expect(result.credits).toBe('0.002')
    expect(quotePrice({ cost: { input: '1e-1000' }, credits: { input: '1e-1000' } }, { input: '1' }).credits).toBe('0.000')
  })

  it('handles quantities and rates above the IEEE safe integer range exactly', () => {
    expect(quotePrice({ cost: { custom: '0.1' }, credits: { custom: '0.001' } }, {
      custom: '9007199254740993',
    })).toMatchObject({ costExact: '900719925474099.3', credits: '9007199254740.993' })
    expect(() => quotePrice(defaults, { input: '-1' })).toThrow('数量')
    expect(() => quotePrice(defaults, { input: '1.2' })).toThrow('数量')
  })

  it('uses the latest upstream model price first, then latest alias price, with no provider-specific key', () => {
    const prices = [
      { model_id: 'alias', version: 9, ...defaults },
      { model_id: 'upstream', version: 1, ...defaults },
      { model_id: 'upstream', version: 3, ...freePriceMaps(defaults.cost, defaults.credits) },
      { model_id: 'provider:upstream', version: 99, ...defaults },
    ]
    expect(resolvePrice(prices, 'upstream', 'alias')).toMatchObject({ source: 'upstream', price: { model_id: 'upstream', version: 3 } })
    expect(resolvePrice(prices, 'missing', 'alias')).toMatchObject({ source: 'alias', price: { model_id: 'alias', version: 9 } })
    expect(resolvePrice(prices, 'missing', 'missing')).toEqual({ source: 'none', price: null })
    expect(suggestNextVersion(prices, 'upstream')).toBe(4)
    expect(suggestNextVersion(prices, 'new')).toBe(1)
  })
})
